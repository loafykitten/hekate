package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/loafykitten/hekate/service/internal/httpx"
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
type RuleMatch struct {
	From     *string `json:"from"`
	Category *string `json:"category"`
}

// RuleSpec is what creates a rule: the approval payload and the tool arguments.
type RuleSpec struct {
	From     string `json:"from,omitempty"`
	Category string `json:"category,omitempty"`
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
	return sp, nil
}

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
	return l
}

// senderLabel words a sender key for approval titles: "example.com" or "a@example.com".
func senderLabel(key string) string { return strings.TrimPrefix(key, "@") }

const ruleCols = `id, match_from, match_category, action, folder, label, hits, last_hit_at, created_at`

func scanRule(sc scanner) (Rule, error) {
	var r Rule
	var id int64
	var from, cat, folder, last sql.NullString
	if err := sc.Scan(&id, &from, &cat, &r.Action, &folder, &r.Label, &r.Hits, &last, &r.CreatedAt); err != nil {
		return r, err //nolint:wrapcheck // callers add context
	}
	r.ID = strconv.FormatInt(id, 10)
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
		WHERE COALESCE(match_from, '') = ? AND COALESCE(match_category, '') = ? AND action = ? AND COALESCE(folder, '') = ?`,
		sp.From, sp.Category, sp.Action, sp.Folder)
	if existing, err := scanRow2(row); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Rule{}, fmt.Errorf("create mail rule: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mail_rules (match_from, match_category, action, folder, label, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, nullable(sp.From), nullable(sp.Category), sp.Action, nullable(sp.Folder), sp.label(), stamp(s.now()))
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

// matches reports whether rule applies to a message: every set field must match.
func (r Rule) matches(m row) bool {
	if r.Match.Category != nil && (!m.Category.Valid || m.Category.String != *r.Match.Category) {
		return false
	}
	if r.Match.From != nil && !senderMatches(*r.Match.From, m.FromAddr) {
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

	hits := map[int64][]int64{} // rule id -> message ids
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
		hits[rid] = append(hits[rid], m.ID)
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
		out, err := s.perform(ctx, r.Action, folder, ids, unruledInbox)
		if err != nil {
			// Left unsettled: tried again next sweep, once the cause is fixed.
			s.log.Warn("mail rule failed", "account", a.cfg.Name, "rule", r.ID, "error", err)
			continue
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
