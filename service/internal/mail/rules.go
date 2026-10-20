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

	"github.com/loafykitten/hekate/service/internal/httpx"
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
type RuleMatch struct {
	From     *string `json:"from"`
	Category *string `json:"category"`
}

// RuleException is mail a rule leaves alone even though it matches: every
// field that is set must match. Subject is words the subject contains.
type RuleException struct {
	From     *string `json:"from"`
	Category *string `json:"category"`
	Subject  *string `json:"subject"`
}

// RuleSpec is what creates a rule: the approval payload and the tool arguments.
type RuleSpec struct {
	From     string       `json:"from,omitempty"`
	Category string       `json:"category,omitempty"`
	Action   string       `json:"action"`
	Folder   string       `json:"folder,omitempty"`
	Except   []ExceptSpec `json:"except,omitempty"`
}

// ExceptSpec is one exception as a spec carries it, and as it is stored.
type ExceptSpec struct {
	From     string `json:"from,omitempty"`
	Category string `json:"category,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

const (
	maxExceptions = 10
	maxSubject    = 100 // characters of subject words in one exception
)

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
		return sp, httpx.BadRequest("a rule needs a sender (from) or a category to match")
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
	if len(sp.Except) > maxExceptions {
		return sp, httpx.BadRequest("a rule takes at most %d exceptions", maxExceptions)
	}
	var except []ExceptSpec
	for _, e := range sp.Except {
		e, err := e.normalize()
		if err != nil {
			return sp, err
		}
		if !slices.Contains(except, e) {
			except = append(except, e)
		}
	}
	// One order for the same set, so an identical rule is recognized as such.
	slices.SortFunc(except, func(a, b ExceptSpec) int {
		return strings.Compare(a.From+"\x00"+a.Category+"\x00"+a.Subject, b.From+"\x00"+b.Category+"\x00"+b.Subject)
	})
	sp.Except = except
	return sp, nil
}

// normalize validates an exception and returns it cleaned up.
func (e ExceptSpec) normalize() (ExceptSpec, error) {
	e.From = strings.ToLower(strings.TrimSpace(e.From))
	e.Category = strings.ToLower(strings.TrimSpace(e.Category))
	e.Subject = subjectWords(e.Subject)
	if e.From == "" && e.Category == "" && e.Subject == "" {
		return e, httpx.BadRequest("each exception needs a sender (from), a category or subject words")
	}
	if e.From != "" {
		if err := checkSender(e.From); err != nil {
			return e, err
		}
	}
	if e.Category != "" && !validCategory(e.Category) {
		return e, httpx.BadRequest("category must be one of %s", strings.Join(categories, ", "))
	}
	if utf8.RuneCountInString(e.Subject) > maxSubject || strings.ContainsFunc(e.Subject, unicode.IsControl) {
		return e, httpx.BadRequest("an exception's subject words must be plain text of at most %d characters", maxSubject)
	}
	return e, nil
}

// subjectWords folds text for subject matching: lower case, single spaces.
func subjectWords(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

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

// label words the rule for the owner, e.g. "Archive newsletter mail from example.com".
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
	if sp.Action == ActionMove {
		l += " to " + sp.Folder
	}
	if len(sp.Except) > 0 {
		phrases := make([]string, len(sp.Except))
		for i, e := range sp.Except {
			phrases[i] = e.phrase()
		}
		l += ", unless " + strings.Join(phrases, " or ")
	}
	return l
}

// phrase words an exception for a label: "it's receipt mail from example.com
// and the subject contains “transaction”".
func (e ExceptSpec) phrase() string {
	var parts []string
	if e.Category != "" || e.From != "" {
		p := "it's"
		if e.Category != "" {
			p += " " + strings.ReplaceAll(e.Category, "_", " ") + " mail"
		}
		if e.From != "" {
			p += " from " + strings.TrimPrefix(e.From, "@")
		}
		parts = append(parts, p)
	}
	if e.Subject != "" {
		parts = append(parts, "the subject contains “"+e.Subject+"”")
	}
	return strings.Join(parts, " and ")
}

// senderLabel words a sender key for approval titles: "example.com" or "a@example.com".
func senderLabel(key string) string { return strings.TrimPrefix(key, "@") }

const ruleCols = `id, match_from, match_category, except_json, action, folder, label, hits, last_hit_at, created_at`

func scanRule(sc scanner) (Rule, error) {
	var r Rule
	var id int64
	var from, cat, folder, last sql.NullString
	var except string
	if err := sc.Scan(&id, &from, &cat, &except, &r.Action, &folder, &r.Label, &r.Hits, &last, &r.CreatedAt); err != nil {
		return r, err //nolint:wrapcheck // callers add context
	}
	r.ID = strconv.FormatInt(id, 10)
	var specs []ExceptSpec
	if err := json.Unmarshal([]byte(except), &specs); err != nil {
		return r, fmt.Errorf("rule %d exceptions: %w", id, err)
	}
	r.Except = make([]RuleException, len(specs))
	for i, e := range specs {
		r.Except[i] = RuleException{From: nullable(e.From), Category: nullable(e.Category), Subject: nullable(e.Subject)}
	}
	if from.Valid {
		r.Match.From = &from.String
	}
	if cat.Valid {
		r.Match.Category = &cat.String
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

// CreateRule stores a rule. A rule with the same match, action and folder as
// an existing one replaces that rule's exceptions (keeping its hits); an
// identical rule is returned as it is.
func (s *Service) CreateRule(ctx context.Context, spec RuleSpec) (Rule, error) {
	r, _, err := s.putRule(ctx, spec)
	return r, err
}

// putRule is CreateRule, also saying whether it replaced an existing rule's
// exceptions.
func (s *Service) putRule(ctx context.Context, spec RuleSpec) (Rule, bool, error) {
	if err := s.requireConfigured(); err != nil {
		return Rule{}, false, err
	}
	sp, err := spec.normalize()
	if err != nil {
		return Rule{}, false, err
	}
	except := exceptJSON(sp.Except)
	existing, err := s.sameRule(ctx, sp)
	switch {
	case err == nil:
		if exceptJSON(existing.spec().Except) == except {
			return existing, false, nil
		}
		id, _ := strconv.ParseInt(existing.ID, 10, 64)
		if _, err := s.db.ExecContext(ctx, `UPDATE mail_rules SET except_json = ?, label = ? WHERE id = ?`, except, sp.label(), id); err != nil {
			return Rule{}, false, fmt.Errorf("update mail rule: %w", err)
		}
		r, err := s.ruleByID(ctx, id)
		if err != nil {
			return Rule{}, false, fmt.Errorf("update mail rule: %w", err)
		}
		s.changed()
		return r, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mail_rules (match_from, match_category, except_json, action, folder, label, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, nullable(sp.From), nullable(sp.Category), except, sp.Action, nullable(sp.Folder), sp.label(), stamp(s.now()))
	if err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	id, _ := res.LastInsertId()
	r, err := s.ruleByID(ctx, id)
	if err != nil {
		return Rule{}, false, fmt.Errorf("create mail rule: %w", err)
	}
	s.changed()
	return r, false, nil
}

// sameRule finds the rule with a spec's match, action and folder, whatever its
// exceptions.
func (s *Service) sameRule(ctx context.Context, sp RuleSpec) (Rule, error) {
	return scanRow2(s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules
		WHERE COALESCE(match_from, '') = ? AND COALESCE(match_category, '') = ? AND action = ? AND COALESCE(folder, '') = ?`,
		sp.From, sp.Category, sp.Action, sp.Folder))
}

func (s *Service) ruleByID(ctx context.Context, id int64) (Rule, error) {
	return scanRow2(s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM mail_rules WHERE id = ?`, id))
}

// exceptJSON encodes normalized exceptions for storage; none is "[]".
func exceptJSON(except []ExceptSpec) string {
	if len(except) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(except)
	return string(b)
}

// spec turns a stored rule back into the spec that makes it.
func (r Rule) spec() RuleSpec {
	sp := RuleSpec{Action: r.Action, From: deref(r.Match.From), Category: deref(r.Match.Category), Folder: deref(r.Folder)}
	for _, e := range r.Except {
		sp.Except = append(sp.Except, ExceptSpec{From: deref(e.From), Category: deref(e.Category), Subject: deref(e.Subject)})
	}
	return sp
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// matches reports whether rule applies to a message: no exception matches it,
// and every set field of the rule's match does.
func (r Rule) matches(m row) bool {
	for _, e := range r.Except {
		if e.matches(m) {
			return false
		}
	}
	if r.Match.Category != nil && (!m.Category.Valid || m.Category.String != *r.Match.Category) {
		return false
	}
	if r.Match.From != nil && !senderMatches(*r.Match.From, m.FromAddr) {
		return false
	}
	return true
}

// matches reports whether an exception covers a message: every set field must
// match. Mail with no category counts as matching a category, so a rule never
// acts on a guess about what the mail is.
func (e RuleException) matches(m row) bool {
	if e.Category != nil && m.Category.Valid && m.Category.String != *e.Category {
		return false
	}
	if e.From != nil && !senderMatches(*e.From, m.FromAddr) {
		return false
	}
	if e.Subject != nil && !strings.Contains(subjectWords(m.Subject), *e.Subject) {
		return false
	}
	return true
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
// category wait for it. Each hit is recorded.
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

	hits := map[int64][]row{} // rule id -> messages
	var ruleOrder []int
	var settled []int64 // messages no rule needs to look at again
	for _, m := range ready {
		matched := -1
		for i, r := range rules {
			if r.matches(m) {
				matched = i
				break
			}
		}
		if matched < 0 {
			settled = append(settled, m.ID)
			continue
		}
		rid, _ := strconv.ParseInt(rules[matched].ID, 10, 64)
		if _, ok := hits[rid]; !ok {
			ruleOrder = append(ruleOrder, matched)
		}
		hits[rid] = append(hits[rid], m)
	}
	if err := s.settle(ctx, settled); err != nil {
		return 0, err
	}
	applied := 0
	for _, i := range ruleOrder {
		r := rules[i]
		rid, _ := strconv.ParseInt(r.ID, 10, 64)
		msgs := hits[rid]
		ids := make([]int64, len(msgs))
		for j, m := range msgs {
			ids[j] = m.ID
		}
		folder := ""
		if r.Folder != nil {
			folder = *r.Folder
		}
		// Checked again under the account's lock, right before acting: the owner
		// may have deleted the rule, changed its exceptions, or filed the mail,
		// while this waited.
		live := func(m row) bool { return unruledInbox(m) && s.ruleStillMatches(ctx, rid, m) }
		out, err := s.perform(ctx, r.Action, folder, ids, live)
		if err != nil {
			// Left unsettled: tried again next sweep, once the cause is fixed.
			s.log.Warn("mail rule failed", "account", a.cfg.Name, "rule", r.ID, "error", err)
			continue
		}
		// Mail the rule no longer covers (it was deleted, or an exception was
		// added meanwhile) stays unsettled: the other rules get another look next sweep.
		var done []int64
		for _, m := range msgs {
			if s.ruleStillMatches(ctx, rid, m) {
				done = append(done, m.ID)
			}
		}
		if err := s.settle(ctx, done); err != nil {
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

// ruleStillMatches reports whether a rule is still in force and still covers a
// message. A failed lookup counts as no: a rule never acts on a guess.
func (s *Service) ruleStillMatches(ctx context.Context, id int64, m row) bool {
	r, err := s.ruleByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		s.log.Warn("cannot check a mail rule", "rule", id, "error", err)
		return false
	}
	return r.matches(m)
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
