package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/httpx"
)

// maxApprovalIDs bounds a payload, however many ids an old approval holds.
const maxApprovalIDs = 500

// Approval payloads. They hold Hekate ids and fixed fields only: nothing a
// message says ever becomes part of what runs.
type (
	idsPayload struct {
		IDs []int64 `json:"ids"`
	}
	idPayload struct {
		ID int64 `json:"id"`
	}
	bulkPayload struct {
		Action string  `json:"action"` // archive, move, mark_read or mark_unread
		IDs    []int64 `json:"ids"`
		Folder string  `json:"folder,omitempty"`
	}
)

func decodePayload(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad approval payload: %w", err)
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// summarize words an Outcome, e.g. "Moved 3 messages to Trash". A run that did
// nothing is an error: the owner should hear that it failed.
func summarize(verb string, out Outcome, suffix string) (string, error) {
	if out.Done == 0 {
		return "", errors.New("those messages are no longer on the mail server")
	}
	msg := verb + " " + plural(out.Done, "message", "messages") + suffix
	if out.Missing > 0 {
		msg += fmt.Sprintf(" (%d no longer on the server)", out.Missing)
	}
	return msg, nil
}

func checkIDs(ids []int64) error {
	if len(ids) == 0 {
		return errors.New("the approval holds no messages")
	}
	if len(ids) > maxApprovalIDs {
		return fmt.Errorf("the approval holds too many messages (%d)", len(ids))
	}
	return nil
}

func (s *Service) execTrash(ctx context.Context, raw json.RawMessage) (string, error) {
	var p idsPayload
	if err := decodePayload(raw, &p); err != nil {
		return "", err
	}
	if err := checkIDs(p.IDs); err != nil {
		return "", err
	}
	out, err := s.Trash(ctx, p.IDs)
	if err != nil {
		return "", err
	}
	s.changed()
	return summarize("Moved", out, " to Trash")
}

func (s *Service) execBulk(ctx context.Context, raw json.RawMessage) (string, error) {
	var p bulkPayload
	if err := decodePayload(raw, &p); err != nil {
		return "", err
	}
	if err := checkIDs(p.IDs); err != nil {
		return "", err
	}
	var verb, suffix string
	switch p.Action {
	case ActionArchive:
		verb = "Archived"
	case ActionMove:
		verb, suffix = "Moved", " to "+p.Folder
	case ActionMarkRead:
		verb, suffix = "Marked", " read"
	case actionMarkUnread:
		verb, suffix = "Marked", " unread"
	default:
		return "", fmt.Errorf("unknown bulk action %q", p.Action)
	}
	out, err := s.perform(ctx, p.Action, p.Folder, p.IDs, nil)
	if err != nil {
		return "", err
	}
	s.changed()
	return summarize(verb, out, suffix)
}

func (s *Service) execUnsubscribe(ctx context.Context, raw json.RawMessage) (string, error) {
	var p idPayload
	if err := decodePayload(raw, &p); err != nil {
		return "", err
	}
	r, err := s.rowByID(ctx, p.ID)
	if err != nil {
		return "", err
	}
	if err := s.Unsubscribe(ctx, p.ID); err != nil {
		return "", err
	}
	return "Sent the unsubscribe request to " + senderLabel(r.FromAddr), nil
}

func (s *Service) execRuleCreate(ctx context.Context, raw json.RawMessage) (string, error) {
	var p RuleSpec
	if err := decodePayload(raw, &p); err != nil {
		return "", err
	}
	r, replaced, err := s.putRule(ctx, p)
	if err != nil {
		return "", err
	}
	if replaced {
		return "Updated the rule: " + r.Label, nil
	}
	return "Created the rule: " + r.Label, nil
}

// approvalCard is the card for an approval.
func approvalCard(a approvals.Approval) *agent.Card {
	data, err := json.Marshal(a)
	if err != nil {
		return nil
	}
	return &agent.Card{Kind: "approval", ID: a.ID, Title: a.Title, Data: data}
}

// ask files an approval request and tells the model what happened: either the
// owner has been asked, or a standing rule let it run at once.
func (s *Service) ask(ctx context.Context, r approvals.Request) (agent.Result, error) {
	a, err := s.appr.Request(ctx, r)
	if err != nil {
		return agent.Result{}, fmt.Errorf("ask for approval: %w", err)
	}
	res := agent.Result{Card: approvalCard(a)}
	result := ""
	if a.Result != nil {
		result = *a.Result
	}
	byRule := a.DecidedBy != nil && *a.DecidedBy == approvals.DecidedByRule
	switch {
	case a.Status == approvals.StatusDone && byRule:
		res.Content = "Done (standing rule): " + result
	case a.Status == approvals.StatusFailed:
		res.Content = "It was allowed but failed: " + result + ". Tell the user."
	default:
		res.Content = fmt.Sprintf("Asked %s to approve (approval #%s); it will run when they allow it. Do not do it another way meanwhile. "+
			"This only says you asked: on later turns, Approvals in your prompt says whether it is still waiting.", s.owner, a.ID)
	}
	return res, nil
}

// sharedSender finds the scope for approving actions on these messages: one
// address, else one domain, else none ("" means the approval can't become a
// standing rule).
func sharedSender(rows []row) (key string) {
	if len(rows) == 0 {
		return ""
	}
	addr, domain := rows[0].FromAddr, domainOf(rows[0].FromAddr)
	sameAddr, sameDomain := addr != "", domain != ""
	for _, r := range rows[1:] {
		sameAddr = sameAddr && r.FromAddr == addr
		sameDomain = sameDomain && domainOf(r.FromAddr) == domain
	}
	switch {
	case sameAddr:
		return addr
	case sameDomain:
		return "@" + domain
	}
	return ""
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return ""
}

// describeRows lists up to five senders and subjects for an approval's detail,
// flattened and clipped because they come from the mail.
func describeRows(rows []row) string {
	var lines []string
	for i, r := range rows {
		if i == 5 {
			lines = append(lines, fmt.Sprintf("… and %d more", len(rows)-5))
			break
		}
		lines = append(lines, oneLine(senderOf(r), 60)+": "+oneLine(r.Subject, 80))
	}
	return strings.Join(lines, "\n")
}

func senderOf(r row) string {
	if r.FromName != "" {
		return r.FromName
	}
	return r.FromAddr
}

// requireRows loads rows for ids, de-duplicated, with a friendly error.
func (s *Service) requireRows(ctx context.Context, ids []int64) ([]row, error) {
	if err := s.requireConfigured(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, httpx.BadRequest("ids is empty")
	}
	return s.rowsByID(ctx, ids)
}
