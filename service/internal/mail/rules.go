package mail

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-msgauth/dkim"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

const (
	minRuleWords  = 3        // shortest subject or body words a rule takes, in characters
	maxRuleWords  = 100      // longest
	ruleBodyBatch = 20       // messages whose text the rules read at a time
	ruleBodyBytes = 64 << 10 // how much of a message the body words are looked for in
	unsubRawBytes = 4 << 20  // how much of a message is read to check its signature; larger ones aren't unsubscribed
	maxExceptions = 5        // per rule
	// maxCachedSubject is how much of a subject the cache keeps; the sweep clips longer ones.
	maxCachedSubject = 500
)

// Rule is the API shape from PROTOCOL.md: a standing instruction the sweep
// applies to new inbox mail.
type Rule struct {
	ID        string          `json:"id"`
	Match     RuleMatch       `json:"match"`
	Except    []RuleException `json:"except"`
	Action    string          `json:"action"`
	Folder    *string         `json:"folder"`
	Label     string          `json:"label"`
	Hits      int             `json:"hits"`
	LastHitAt *string         `json:"last_hit_at"`
	CreatedAt string          `json:"created_at"`
}

// RuleMatch says which mail a rule covers; every field that is set must match.
// Subject and Body are words (a phrase) the subject or the body must contain.
type RuleMatch struct {
	From     *string `json:"from"`
	Category *string `json:"category"`
	Subject  *string `json:"subject"`
	Body     *string `json:"body"`
}

// RuleException is mail a rule spares: every field that is set must match.
type RuleException struct {
	From     string `json:"from,omitempty"`
	Category string `json:"category,omitempty"`
	Subject  string `json:"subject,omitempty"` // words the subject contains, any case
}

// RuleSpec is what creates a rule: the approval payload and the tool arguments.
type RuleSpec struct {
	From     string          `json:"from,omitempty"`
	Category string          `json:"category,omitempty"`
	Subject  string          `json:"subject,omitempty"`
	Body     string          `json:"body,omitempty"`
	Except   []RuleException `json:"except,omitempty"`
	Action   string          `json:"action"`
	Folder   string          `json:"folder,omitempty"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// normalize validates a spec and returns it cleaned up.
func (sp RuleSpec) normalize() (RuleSpec, error) {
	sp.From = strings.ToLower(strings.TrimSpace(sp.From))
	sp.Category = strings.ToLower(strings.TrimSpace(sp.Category))
	sp.Action = strings.ToLower(strings.TrimSpace(sp.Action))
	sp.Folder = strings.TrimSpace(sp.Folder)
	if sp.From == "" && sp.Category == "" {
		return sp, httpx.BadRequest("a rule needs a sender (from) or a category to match; subject and body words only narrow it")
	}
	var err error
	if sp.Subject, err = ruleWords("subject", sp.Subject); err != nil {
		return sp, err
	}
	if sp.Body, err = ruleWords("body", sp.Body); err != nil {
		return sp, err
	}
	if sp.Except, err = normalizeExceptions(sp.Except); err != nil {
		return sp, err
	}
	if sp.From != "" {
		if err := checkSender(sp.From); err != nil {
			return sp, err
		}
	}
	if sp.Category != "" && !validCategory(sp.Category) {
		return sp, httpx.BadRequest("category must be one of %s", strings.Join(categories, ", "))
	}
	switch sp.Action {
	case ActionArchive, ActionTrash, ActionMarkRead:
		if sp.Folder != "" {
			return sp, httpx.BadRequest("folder only goes with the move action")
		}
	case ActionMove:
		if sp.Folder == "" {
			return sp, httpx.BadRequest("the move action needs a folder")
		}
		if strings.EqualFold(sp.Folder, inbox) {
			return sp, httpx.BadRequest("moving mail to the inbox is not a rule")
		}
	case ActionUnsubscribe:
		if sp.Folder != "" {
			return sp, httpx.BadRequest("folder only goes with the move action")
		}
		// The owner names who to unsubscribe from: a category comes from the
		// classifier reading untrusted mail, so it alone never decides where
		// requests go.
		if sp.From == "" {
			return sp, httpx.BadRequest("an unsubscribe rule needs a sender (from)")
		}
		// It acts per sender, so body words would only cost fetches.
		if sp.Body != "" {
			return sp, httpx.BadRequest("an unsubscribe rule can't match body words")
		}
	default:
		return sp, httpx.BadRequest("action must be archive, trash, mark_read, move or unsubscribe")
	}
	return sp, nil
}

// ruleWords cleans the words a rule wants in the subject or body: one
// lowercased line, as foldWords makes the text it is looked for in.
func ruleWords(field, s string) (string, error) {
	w := foldWords(s)
	if w == "" {
		return "", nil
	}
	if strings.ContainsFunc(w, unicode.IsControl) {
		return "", httpx.BadRequest("%s words must be plain text", field)
	}
	if n := utf8.RuneCountInString(w); n < minRuleWords || n > maxRuleWords {
		return "", httpx.BadRequest("%s words must be %d to %d characters", field, minRuleWords, maxRuleWords)
	}
	return w, nil
}

// normalizeExceptions cleans a rule's exceptions, drops repeats and sorts them,
// so the same set always reads (and compares) the same.
func normalizeExceptions(in []RuleException) ([]RuleException, error) {
	if len(in) > maxExceptions {
		return nil, httpx.BadRequest("a rule can have at most %d exceptions", maxExceptions)
	}
	var out []RuleException
	for _, e := range in {
		e.From = strings.ToLower(strings.TrimSpace(e.From))
		e.Category = strings.ToLower(strings.TrimSpace(e.Category))
		var err error
		if e.Subject, err = ruleWords("exception subject", e.Subject); err != nil {
			return nil, err
		}
		if e.From == "" && e.Category == "" && e.Subject == "" {
			return nil, httpx.BadRequest("an exception needs a sender (from), a category or subject words")
		}
		if e.From != "" {
			if err := checkSender(e.From); err != nil {
				return nil, err
			}
		}
		if e.Category != "" && !validCategory(e.Category) {
			return nil, httpx.BadRequest("exception category must be one of %s", strings.Join(categories, ", "))
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b RuleException) int {
		return strings.Compare(a.From+"\x00"+a.Category+"\x00"+a.Subject, b.From+"\x00"+b.Category+"\x00"+b.Subject)
	})
	return out, nil
}

// exceptJSON is how a rule's exceptions are stored: nil (NULL) for none.
func exceptJSON(ex []RuleException) *string {
	if len(ex) == 0 {
		return nil
	}
	b, _ := json.Marshal(ex) // plain struct of strings: cannot fail
	s := string(b)
	return &s
}

// foldWords lowercases text and collapses its whitespace, so words match
// whatever their case and line breaks.
func foldWords(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// checkSender accepts an address or "@domain".
func checkSender(from string) error {
	if strings.ContainsAny(from, " \t\r\n<>,;\"'") {
		return httpx.BadRequest("from must be an address like name@example.com or a domain like @example.com")
	}
	at := strings.LastIndex(from, "@")
	if at < 0 || at == len(from)-1 {
		return httpx.BadRequest("from must be an address like name@example.com or a domain like @example.com")
	}
	if at == 0 && !strings.Contains(from[1:], ".") {
		return httpx.BadRequest("from must be an address like name@example.com or a domain like @example.com")
	}
	return nil
}

// label words the rule for the owner, e.g. "Archive newsletter mail from
// example.com", `Trash mail from example.com with "upgrade" in the subject` or
// "Unsubscribe from example.com (promo mail)".
func (sp RuleSpec) label() string {
	var l string
	if sp.Action == ActionUnsubscribe {
		l = "Unsubscribe from " + strings.TrimPrefix(sp.From, "@")
		if sp.Category != "" {
			l += " (" + strings.ReplaceAll(sp.Category, "_", " ") + " mail)"
		}
	} else {
		verb := map[string]string{ActionArchive: "Archive", ActionTrash: "Trash", ActionMarkRead: "Mark as read", ActionMove: "Move"}[sp.Action]
		what := "mail"
		if sp.Category != "" {
			what = strings.ReplaceAll(sp.Category, "_", " ") + " mail"
		}
		l = verb + " " + what
		if sp.From != "" {
			l += " from " + strings.TrimPrefix(sp.From, "@")
		}
	}
	var words []string
	if sp.Subject != "" {
		words = append(words, `"`+sp.Subject+`" in the subject`)
	}
	if sp.Body != "" {
		words = append(words, `"`+sp.Body+`" in the body`)
	}
	if len(words) > 0 {
		l += " with " + strings.Join(words, " and ")
	}
	if sp.Action == ActionMove {
		l += " to " + sp.Folder
	}
	if len(sp.Except) > 0 {
		spares := make([]string, len(sp.Except))
		for i, e := range sp.Except {
			spares[i] = e.label()
		}
		l += ", except " + strings.Join(spares, " or ")
	}
	return l
}

// label words an exception, e.g. "receipts from example.com with “transaction” in the subject".
func (e RuleException) label() string {
	what := "mail"
	if e.Category != "" {
		what = strings.ReplaceAll(e.Category, "_", " ") + " mail"
	}
	if e.From != "" {
		what += " from " + strings.TrimPrefix(e.From, "@")
	}
	if e.Subject != "" {
		what += " with “" + e.Subject + "” in the subject"
	}
	return what
}

// senderLabel words a sender key for approval titles: "example.com" or "a@example.com".
func senderLabel(key string) string { return strings.TrimPrefix(key, "@") }

const ruleCols = `id, match_from, match_category, match_subject, match_body, except_json, action, folder, label, hits, last_hit_at, created_at`

func scanRule(sc scanner) (Rule, error) {
	var r Rule
	var id int64
	var from, cat, subject, body, except, folder, last sql.NullString
	if err := sc.Scan(&id, &from, &cat, &subject, &body, &except, &r.Action, &folder, &r.Label, &r.Hits, &last, &r.CreatedAt); err != nil {
		return r, err //nolint:wrapcheck // callers add context
	}
	r.ID = strconv.FormatInt(id, 10)
	r.Except = []RuleException{}
	if except.Valid {
		if err := json.Unmarshal([]byte(except.String), &r.Except); err != nil {
			return r, fmt.Errorf("rule %d exceptions: %w", id, err)
		}
	}
	if from.Valid {
		r.Match.From = &from.String
	}
	if cat.Valid {
		r.Match.Category = &cat.String
	}
	if subject.Valid {
		r.Match.Subject = &subject.String
	}
	if body.Valid {
		r.Match.Body = &body.String
	}
	if folder.Valid {
		r.Folder = &folder.String
	}
	if last.Valid {
		r.LastHitAt = &last.String
	}
	return r, nil
}

// Rules lists rules, oldest first (the order they are tried in). A rule that
// replaced another keeps the original's place.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleCols+` FROM mail_rules ORDER BY place, id`)
	if err != nil {
		return nil, fmt.Errorf("list mail rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("list mail rules: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// CreateRule stores a rule; see putRule.
func (s *Service) CreateRule(ctx context.Context, spec RuleSpec) (Rule, error) {
	r, _, err := s.putRule(ctx, spec)
	return r, err
}

// putRule stores a rule. An identical rule is returned as it is. A rule with
// the same match and action but other exceptions replaces that one: a new rule
// that keeps its hits, its age and its place in the order, so exceptions never change
// under a sweep that is applying the old rule (to that sweep, the old rule was
// deleted). replaced says so.
func (s *Service) putRule(ctx context.Context, spec RuleSpec) (r Rule, replaced bool, err error) {
	if err := s.requireConfigured(); err != nil {
		return Rule{}, false, err
	}
	sp, err := spec.normalize()
	if err != nil {
		return Rule{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	existing, err := scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules
		WHERE COALESCE(match_from, '') = ? AND COALESCE(match_category, '') = ? AND COALESCE(match_subject, '') = ?
		AND COALESCE(match_body, '') = ? AND action = ? AND COALESCE(folder, '') = ?`,
		sp.From, sp.Category, sp.Subject, sp.Body, sp.Action, sp.Folder))
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	if found && slices.Equal(existing.Except, sp.Except) {
		return existing, false, nil
	}
	created, hits := stamp(s.now()), 0
	var lastHit *string
	var place *int64 // nil: a new rule goes last, at its own id
	if found {
		created, hits, lastHit = existing.CreatedAt, existing.Hits, existing.LastHitAt
		var p int64
		if err := tx.QueryRowContext(ctx, `SELECT place FROM mail_rules WHERE id = ?`, existing.ID).Scan(&p); err != nil {
			return Rule{}, false, fmt.Errorf("replace mail rule: %w", err)
		}
		place = &p
		if _, err := tx.ExecContext(ctx, `DELETE FROM mail_rules WHERE id = ?`, existing.ID); err != nil {
			return Rule{}, false, fmt.Errorf("replace mail rule: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO mail_rules (match_from, match_category, match_subject, match_body, except_json,
		action, folder, label, hits, last_hit_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullable(sp.From), nullable(sp.Category), nullable(sp.Subject), nullable(sp.Body), exceptJSON(sp.Except),
		sp.Action, nullable(sp.Folder), sp.label(), hits, lastHit, created)
	if err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	id, _ := res.LastInsertId()
	if place == nil {
		place = &id
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mail_rules SET place = ? WHERE id = ?`, *place, id); err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	r, err = scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules WHERE id = ?`, id))
	if err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	s.changed()
	return r, found, nil
}

// sameRule finds the rule with the same match and action as spec (normalized),
// whatever its exceptions.
func (s *Service) sameRule(ctx context.Context, sp RuleSpec) (Rule, bool, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules
		WHERE COALESCE(match_from, '') = ? AND COALESCE(match_category, '') = ? AND COALESCE(match_subject, '') = ?
		AND COALESCE(match_body, '') = ? AND action = ? AND COALESCE(folder, '') = ?`,
		sp.From, sp.Category, sp.Subject, sp.Body, sp.Action, sp.Folder))
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, false, nil
	}
	if err != nil {
		return Rule{}, false, fmt.Errorf("find mail rule: %w", err)
	}
	return r, true, nil
}

// DeleteRule removes a rule. Mail it already handled stays handled.
func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mail_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete mail rule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.NotFound("no mail rule %d", id)
	}
	s.changed()
	return nil
}

// matchesHeaders reports whether every field of the rule but its body words
// matches a message.
func (r Rule) matchesHeaders(m row) bool {
	if r.Match.Category != nil && (!m.Category.Valid || m.Category.String != *r.Match.Category) {
		return false
	}
	if r.Match.From != nil && !senderMatches(*r.Match.From, m.FromAddr) {
		return false
	}
	if r.Match.Subject != nil && !strings.Contains(foldWords(m.Subject), *r.Match.Subject) {
		return false
	}
	return true
}

// spares reports whether one of the rule's exceptions covers a message, so the
// rule leaves it alone. Every field an exception sets must match. When it can't
// tell, it spares: mail with no category counts as matching a category, and a
// subject the cache clipped counts as containing the words.
func (r Rule) spares(m row) bool {
	for _, e := range r.Except {
		if e.Category != "" && m.Category.Valid && m.Category.String != e.Category {
			continue
		}
		if e.From != "" && !senderMatches(e.From, m.FromAddr) {
			continue
		}
		if e.Subject != "" && !strings.Contains(foldWords(m.Subject), e.Subject) && !subjectClipped(m.Subject) {
			continue
		}
		return true
	}
	return false
}

// subjectClipped reports whether the cache cut a subject short, so words past
// the cut can't be seen.
func subjectClipped(s string) bool {
	return strings.HasSuffix(s, "…") && utf8.RuneCountInString(s) > maxCachedSubject
}

// needsBody reports whether the rule has to read a message's text to decide.
func (r Rule) needsBody(m row) bool {
	return r.Match.Body != nil && r.matchesHeaders(m) && !r.spares(m)
}

// senderMatches compares a rule's from (an address or "@domain") with an
// address. A domain covers its subdomains.
func senderMatches(from, addr string) bool {
	addr = strings.ToLower(addr)
	if !strings.HasPrefix(from, "@") {
		return addr == from
	}
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return false
	}
	domain := addr[i+1:]
	return domain == from[1:] || strings.HasSuffix(domain, "."+from[1:])
}

// applyRules runs the rules over the account's inbox mail they have not seen.
// A message is ready once it is classified, or classification has given up on
// it, or it is too old to classify, or there is no model; rules that need a
// category wait for it, and rules with body words read the text of the mail
// that matches them on everything else. The first filing rule that matches a
// message takes it; unsubscribe rules file nothing, so they run afterwards on
// the mail the filing rules settled. Each hit is recorded.
func (s *Service) applyRules(ctx context.Context, a *account) (int, error) {
	rules, err := s.Rules(ctx)
	if err != nil {
		return 0, err
	}
	modelOff := s.llm == nil || !s.llm.Configured()
	rows, err := s.db.QueryContext(ctx, `SELECT `+rowCols+` FROM mail_messages
		WHERE account = ? AND folder = 'INBOX' AND gone = 0 AND rules_applied = 0
		AND (classified_at IS NOT NULL OR classify_attempts >= ? OR date < ? OR ?)`,
		a.cfg.Name, classifyTries, stamp(s.now().AddDate(0, 0, -classifyDays)), modelOff)
	if err != nil {
		return 0, fmt.Errorf("find mail for rules: %w", err)
	}
	var ready []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("find mail for rules: %w", err)
		}
		ready = append(ready, r)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, fmt.Errorf("find mail for rules: %w", err)
	}

	hits := map[int64][]int64{} // rule id -> message ids
	var ruleOrder []int
	var settled []int64      // messages no rule needs to look at again
	due := map[int64]int64{} // message id -> the unsubscribe rule due to act on it
	for batch := range slices.Chunk(ready, ruleBodyBatch) {
		bodies := s.ruleBodies(ctx, a, batch, rules)
		maps.Copy(due, s.unsubDue(ctx, a, batch, rules))
	messages:
		for _, m := range batch {
			matched := -1
			for i, r := range rules {
				if r.Action == ActionUnsubscribe || !r.matchesHeaders(m) || r.spares(m) {
					continue // a spared message is this rule's to skip, without reading its body
				}
				if r.Match.Body != nil {
					text, ok := bodies[m.ID]
					if !ok {
						continue messages // unreadable now: left for the next sweep
					}
					if !strings.Contains(text, *r.Match.Body) {
						continue
					}
				}
				matched = i
				break
			}
			if matched < 0 {
				settled = append(settled, m.ID)
				continue
			}
			rid, _ := strconv.ParseInt(rules[matched].ID, 10, 64)
			if _, ok := hits[rid]; !ok {
				ruleOrder = append(ruleOrder, matched)
			}
			hits[rid] = append(hits[rid], m.ID)
		}
	}
	// Recorded before anything is settled, so an interrupted sweep still sends them.
	for id, rid := range due {
		if _, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET unsub_rule = ? WHERE id = ? AND rules_applied = 0`, rid, id); err != nil {
			return 0, fmt.Errorf("record a due unsubscribe: %w", err)
		}
	}
	if err := s.settle(ctx, settled); err != nil {
		return 0, err
	}
	applied := 0
	for _, i := range ruleOrder {
		r := rules[i]
		rid, _ := strconv.ParseInt(r.ID, 10, 64)
		ids := hits[rid]
		folder := ""
		if r.Folder != nil {
			folder = *r.Folder
		}
		// Checked again under the account's lock, right before acting: the owner
		// may have deleted the rule, or filed the mail, while this waited.
		live := func(m row) bool { return unruledInbox(m) && s.ruleExists(ctx, rid) }
		out, err := s.perform(ctx, r.Action, folder, ids, live)
		if err != nil {
			// Left unsettled: tried again next sweep, once the cause is fixed.
			s.log.Warn("mail rule failed", "account", a.cfg.Name, "rule", r.ID, "error", err)
			continue
		}
		if !s.ruleExists(ctx, rid) {
			continue // deleted meanwhile: the other rules get another look next sweep
		}
		if err := s.settle(ctx, ids); err != nil {
			return applied, err
		}
		n := out.Done
		if n > 0 {
			if _, err := s.db.ExecContext(ctx, `UPDATE mail_rules SET hits = hits + ?, last_hit_at = ? WHERE id = ?`, n, stamp(s.now()), rid); err != nil {
				return applied, fmt.Errorf("record mail rule hit: %w", err)
			}
		}
		applied += n
	}
	n, err := s.unsubscribeDue(ctx, a)
	return applied + n, err
}

// unsubDue finds the messages in batch an unsubscribe rule should act on:
// mail it matches that offers one-click unsubscribe with those headers signed
// by the sender's own domain (RFC 8058, section 4). Anyone can put a sender's
// address on a message and a URL of their choosing in its headers; only a valid
// DKIM signature from that domain, covering them, shows the sender asked for
// the request. Mail that isn't signed so, or can't be checked right now, is
// left alone: the sender's next message tries again.
func (s *Service) unsubDue(ctx context.Context, a *account, batch []row, rules []Rule) map[int64]int64 {
	ruleFor := map[int64]int64{}
	byValidity := map[uint32][]row{}
	for _, m := range batch {
		if !m.canUnsubscribe() {
			continue
		}
		i := slices.IndexFunc(rules, func(r Rule) bool { return r.Action == ActionUnsubscribe && r.matchesHeaders(m) && !r.spares(m) })
		if i < 0 {
			continue
		}
		ruleFor[m.ID], _ = strconv.ParseInt(rules[i].ID, 10, 64)
		byValidity[m.Validity] = append(byValidity[m.Validity], m)
	}
	due := map[int64]int64{}
	for validity, ms := range byValidity {
		uids := make([]imap.UID, len(ms))
		for i, m := range ms {
			uids[i] = imap.UID(m.UID)
		}
		var raws map[imap.UID][]byte
		err := s.run(ctx, a, func(c *imapclient.Client) error {
			var err error
			raws, err = fetchRaw(c, inbox, validity, uids, unsubRawBytes)
			return err
		})
		if err != nil {
			if !errors.Is(err, ErrGone) && ctx.Err() == nil {
				s.log.Warn("cannot read mail to check its unsubscribe signature", "account", a.cfg.Name, "error", err)
			}
			continue
		}
		for _, m := range ms {
			if s.signedUnsubscribe(raws[imap.UID(m.UID)], m.FromAddr) {
				due[m.ID] = ruleFor[m.ID]
			} else {
				s.log.Info("unsubscribe rule left unsigned mail alone", "account", a.cfg.Name, "rule", ruleFor[m.ID], "message", m.ID)
			}
		}
	}
	return due
}

// signedUnsubscribe reports whether raw carries a valid DKIM signature by the
// sender's domain (or a parent of it) that covers From, List-Unsubscribe and
// List-Unsubscribe-Post.
func (s *Service) signedUnsubscribe(raw []byte, fromAddr string) bool {
	if len(raw) == 0 {
		return false
	}
	at := strings.LastIndex(fromAddr, "@")
	if at < 0 {
		return false
	}
	sender := strings.ToLower(fromAddr[at+1:])
	vs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{LookupTXT: s.lookupTXT, MaxVerifications: 5})
	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		return false
	}
	for _, v := range vs {
		if v.Err != nil {
			continue
		}
		d := strings.ToLower(v.Domain)
		if sender != d && !strings.HasSuffix(sender, "."+d) {
			continue
		}
		signed := map[string]bool{}
		for _, k := range v.HeaderKeys {
			signed[strings.ToLower(k)] = true
		}
		if signed["from"] && signed["list-unsubscribe"] && signed["list-unsubscribe-post"] {
			return true
		}
	}
	return false
}

// unsubscribeDue sends the one-click unsubscribe request for the account's
// settled mail an unsubscribe rule is due to act on. Each message gets one try:
// the mark is cleared once it has been tried (a failure is logged, and the
// sender's next message tries again), but survives a sweep cut short. A
// sender's list asked within unsubEvery in this inbox is not asked again, so a
// burst of mail sent before the request took effect doesn't become a burst of
// requests.
func (s *Service) unsubscribeDue(ctx context.Context, a *account) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, unsub_rule FROM mail_messages
		WHERE account = ? AND unsub_rule IS NOT NULL AND rules_applied = 1 ORDER BY id`, a.cfg.Name)
	if err != nil {
		return 0, fmt.Errorf("find due unsubscribes: %w", err)
	}
	type dueMsg struct{ id, rule int64 }
	var list []dueMsg
	for rows.Next() {
		var d dueMsg
		if err := rows.Scan(&d.id, &d.rule); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("find due unsubscribes: %w", err)
		}
		list = append(list, d)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, fmt.Errorf("find due unsubscribes: %w", err)
	}
	n := 0
	for _, d := range list {
		if ctx.Err() != nil {
			return n, nil //nolint:nilerr // stopping: the rest stay due for the next sweep
		}
		ok, err := s.unsubscribeOne(ctx, d.id, d.rule)
		if err != nil {
			return n, err
		}
		if ctx.Err() != nil {
			return n, nil //nolint:nilerr // cut short mid-request: it stays due and is tried again
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET unsub_rule = NULL WHERE id = ?`, d.id); err != nil {
			return n, fmt.Errorf("clear a due unsubscribe: %w", err)
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// unsubscribeOne tries one due unsubscribe and records the rule's hit. It
// reports whether the request went through.
func (s *Service) unsubscribeOne(ctx context.Context, id, rid int64) (bool, error) {
	if !s.ruleExists(ctx, rid) {
		return false, nil
	}
	m, err := s.rowByID(ctx, id)
	if err != nil || !m.canUnsubscribe() {
		return false, nil //nolint:nilerr // gone, or unsubscribed by hand meanwhile: nothing to send
	}
	recent, err := s.unsubscribedLately(ctx, m)
	if err != nil || recent {
		return false, err
	}
	if err := s.Unsubscribe(ctx, id); err != nil {
		s.log.Warn("mail rule could not unsubscribe", "account", m.Account, "rule", rid, "error", err)
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mail_rules SET hits = hits + 1, last_hit_at = ? WHERE id = ?`, stamp(s.now()), rid); err != nil {
		return true, fmt.Errorf("record mail rule hit: %w", err)
	}
	return true, nil
}

// unsubscribedLately reports whether mail from m's sender and list was
// unsubscribed from within unsubEvery, by a rule or by the owner, in the same
// account: each inbox's subscription is its own.
func (s *Service) unsubscribedLately(ctx context.Context, m row) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_messages
		WHERE account = ? AND from_addr = ? AND list_id = ? AND unsubscribed_at >= ?`,
		m.Account, m.FromAddr, m.ListID, stamp(s.now().Add(-unsubEvery))).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check earlier unsubscribes: %w", err)
	}
	return n > 0, nil
}

// ruleBodies reads the text of the messages in batch that a rule with body
// words matches on everything else, folded by foldWords. The text lives in
// memory for this sweep and is never stored. A message missing from the result
// could not be read now; the rules try it again next sweep.
func (s *Service) ruleBodies(ctx context.Context, a *account, batch []row, rules []Rule) map[int64]string {
	byValidity := map[uint32][]row{}
	for _, m := range batch {
		if slices.ContainsFunc(rules, func(r Rule) bool { return r.needsBody(m) }) {
			byValidity[m.Validity] = append(byValidity[m.Validity], m)
		}
	}
	out := map[int64]string{}
	for validity, ms := range byValidity {
		uids := make([]imap.UID, len(ms))
		for i, m := range ms {
			uids[i] = imap.UID(m.UID)
		}
		var raws map[imap.UID][]byte
		err := s.run(ctx, a, func(c *imapclient.Client) error {
			var err error
			raws, err = fetchRaw(c, inbox, validity, uids, ruleBodyBytes)
			return err
		})
		if err != nil {
			if !errors.Is(err, ErrGone) && ctx.Err() == nil {
				s.log.Warn("cannot read mail for a rule", "account", a.cfg.Name, "error", err)
			}
			continue
		}
		for _, m := range ms {
			raw := raws[imap.UID(m.UID)]
			if len(raw) == 0 {
				continue // gone from the server: the next sync notices
			}
			text, err := bodyText(raw)
			if err != nil {
				text = "" // no readable text holds no words
			}
			out[m.ID] = foldWords(text)
		}
	}
	return out
}

// ruleExists reports whether a rule is still in force. A failed lookup counts
// as no: a rule never acts on a guess.
func (s *Service) ruleExists(ctx context.Context, id int64) bool {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM mail_rules WHERE id = ?`, id).Scan(&n); err != nil {
		s.log.Warn("cannot check a mail rule", "rule", id, "error", err)
		return false
	}
	return n > 0
}

// settle marks messages as handled by the rules.
func (s *Service) settle(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET rules_applied = 1 WHERE id = ?`, id); err != nil {
			return fmt.Errorf("update mail: %w", err)
		}
	}
	return nil
}

// payloadOf encodes an approval payload.
func payloadOf(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
