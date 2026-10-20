package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

const (
	minRuleWords  = 3        // shortest subject or body words a rule takes, in characters
	maxRuleWords  = 100      // longest
	ruleBodyBatch = 20       // messages whose text the rules read at a time
	ruleBodyBytes = 64 << 10 // how much of a message the body words are looked for in
)

// Rule is the API shape from PROTOCOL.md: a standing instruction the sweep
// applies to new inbox mail.
type Rule struct {
	ID        string    `json:"id"`
	Match     RuleMatch `json:"match"`
	Action    string    `json:"action"`
	Folder    *string   `json:"folder"`
	Label     string    `json:"label"`
	Hits      int       `json:"hits"`
	LastHitAt *string   `json:"last_hit_at"`
	CreatedAt string    `json:"created_at"`
}

// RuleMatch says which mail a rule covers; every field that is set must match.
// Subject and Body are words (a phrase) the subject or the body must contain.
type RuleMatch struct {
	From     *string `json:"from"`
	Category *string `json:"category"`
	Subject  *string `json:"subject"`
	Body     *string `json:"body"`
}

// RuleSpec is what creates a rule: the approval payload and the tool arguments.
type RuleSpec struct {
	From     string `json:"from,omitempty"`
	Category string `json:"category,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Body     string `json:"body,omitempty"`
	Action   string `json:"action"`
	Folder   string `json:"folder,omitempty"`
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
	default:
		return sp, httpx.BadRequest("action must be archive, trash, mark_read or move")
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
// example.com" or `Trash mail from example.com with "upgrade" in the subject`.
func (sp RuleSpec) label() string {
	verb := map[string]string{ActionArchive: "Archive", ActionTrash: "Trash", ActionMarkRead: "Mark as read", ActionMove: "Move"}[sp.Action]
	what := "mail"
	if sp.Category != "" {
		what = strings.ReplaceAll(sp.Category, "_", " ") + " mail"
	}
	l := verb + " " + what
	if sp.From != "" {
		l += " from " + strings.TrimPrefix(sp.From, "@")
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
	return l
}

// senderLabel words a sender key for approval titles: "example.com" or "a@example.com".
func senderLabel(key string) string { return strings.TrimPrefix(key, "@") }

const ruleCols = `id, match_from, match_category, match_subject, match_body, action, folder, label, hits, last_hit_at, created_at`

func scanRule(sc scanner) (Rule, error) {
	var r Rule
	var id int64
	var from, cat, subject, body, folder, last sql.NullString
	if err := sc.Scan(&id, &from, &cat, &subject, &body, &r.Action, &folder, &r.Label, &r.Hits, &last, &r.CreatedAt); err != nil {
		return r, err //nolint:wrapcheck // callers add context
	}
	r.ID = strconv.FormatInt(id, 10)
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

// Rules lists rules, oldest first (the order they are tried in).
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleCols+` FROM mail_rules ORDER BY id`)
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

// CreateRule stores a rule. An identical rule is returned as it is.
func (s *Service) CreateRule(ctx context.Context, spec RuleSpec) (Rule, error) {
	if err := s.requireConfigured(); err != nil {
		return Rule{}, err
	}
	sp, err := spec.normalize()
	if err != nil {
		return Rule{}, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules
		WHERE COALESCE(match_from, '') = ? AND COALESCE(match_category, '') = ? AND COALESCE(match_subject, '') = ?
		AND COALESCE(match_body, '') = ? AND action = ? AND COALESCE(folder, '') = ?`,
		sp.From, sp.Category, sp.Subject, sp.Body, sp.Action, sp.Folder)
	if existing, err := scanRow2(row); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Rule{}, fmt.Errorf("create mail rule: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mail_rules (match_from, match_category, match_subject, match_body, action, folder, label, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, nullable(sp.From), nullable(sp.Category), nullable(sp.Subject), nullable(sp.Body),
		sp.Action, nullable(sp.Folder), sp.label(), stamp(s.now()))
	if err != nil {
		return Rule{}, fmt.Errorf("create mail rule: %w", err)
	}
	id, _ := res.LastInsertId()
	r, err := scanRow2(s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules WHERE id = ?`, id))
	if err != nil {
		return Rule{}, fmt.Errorf("create mail rule: %w", err)
	}
	s.changed()
	return r, nil
}

func scanRow2(row *sql.Row) (Rule, error) { return scanRule(row) }

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

// needsBody reports whether the rule has to read a message's text to decide.
func (r Rule) needsBody(m row) bool { return r.Match.Body != nil && r.matchesHeaders(m) }

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
// that matches them on everything else. Each hit is recorded.
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
	var settled []int64 // messages no rule needs to look at again
	for batch := range slices.Chunk(ready, ruleBodyBatch) {
		bodies := s.ruleBodies(ctx, a, batch, rules)
	messages:
		for _, m := range batch {
			matched := -1
			for i, r := range rules {
				if !r.matchesHeaders(m) {
					continue
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
	return applied, nil
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
