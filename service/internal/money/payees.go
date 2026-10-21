package money

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/httpx"
)

// What the owner can say a recurring payee is. Detection only says a payment
// looks recurring; these labels are the owner's word, kept by payee id.
const (
	kindBill         = "bill"
	kindSubscription = "subscription"
	kindIncome       = "income"
	kindNotRecurring = "not_recurring"
	kindUnknown      = "unknown" // not stored: removes the label

	maxNoteLen = 100
)

// payeeLabel is the owner's answer about one payee.
type payeeLabel struct {
	kind string
	note string
}

// payeeID is a short stable id for a payee and the direction of its money. It is
// derived from the normalized payee key, so the payee's text is never stored.
func payeeID(key string, income bool) string {
	dir := "out:"
	if income {
		dir = "in:"
	}
	sum := sha256.Sum256([]byte(dir + key))
	return "p" + hex.EncodeToString(sum[:])[:10]
}

// labels loads every label the owner has given.
func (s *Service) labels(ctx context.Context) (map[string]payeeLabel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payee_id, kind, note FROM money_payees`)
	if err != nil {
		return nil, fmt.Errorf("read payee labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]payeeLabel{}
	for rows.Next() {
		var id string
		var l payeeLabel
		if err := rows.Scan(&id, &l.kind, &l.note); err != nil {
			return nil, fmt.Errorf("read a payee label: %w", err)
		}
		out[id] = l
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// seenPayees maps the id of every payee in the stored transactions to whether it
// is money in.
func (s *Service) seenPayees(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT name, merchant, amount_cents > 0 FROM money_transactions`)
	if err != nil {
		return nil, fmt.Errorf("read payees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var name, merchant string
		var in bool
		if err := rows.Scan(&name, &merchant, &in); err != nil {
			return nil, fmt.Errorf("read a payee: %w", err)
		}
		r := txRow{name: name, merchant: merchant}
		if k := payeeKey(r.payee()); k != "" {
			out[payeeID(k, in)] = in
		}
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// LabelPayee records the owner's answer about a payee (by the id money_recurring
// shows), or with kind "unknown" removes it. It says what it did, in words that
// carry no bank text.
func (s *Service) LabelPayee(ctx context.Context, id, kind, note string) (string, error) {
	id, kind = strings.TrimSpace(id), strings.ToLower(strings.TrimSpace(kind))
	note = oneLine(note, 10*maxNoteLen)
	switch kind {
	case kindBill, kindSubscription, kindIncome, kindNotRecurring, kindUnknown:
	default:
		return "", httpx.BadRequest("kind must be bill, subscription, income, not_recurring or unknown")
	}
	if utf8.RuneCountInString(note) > maxNoteLen {
		return "", httpx.BadRequest("note is too long: keep it to %d characters", maxNoteLen)
	}
	seen, err := s.seenPayees(ctx)
	if err != nil {
		return "", err
	}
	income, ok := seen[id]
	if !ok {
		return "", httpx.NotFound("no payee has the id %q: use the ids money_recurring shows", oneLine(id, 20))
	}
	switch {
	case kind == kindIncome && !income:
		return "", httpx.BadRequest("that payee is money going out, so it can't be income")
	case (kind == kindBill || kind == kindSubscription) && income:
		return "", httpx.BadRequest("that payee is money coming in, so it can't be a %s", kind)
	}
	if kind == kindUnknown {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM money_payees WHERE payee_id = ?`, id); err != nil {
			return "", fmt.Errorf("clear payee label: %w", err)
		}
		s.changed()
		return fmt.Sprintf("Cleared the label on payee %s: it counts as unconfirmed again.", id), nil
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO money_payees (payee_id, kind, note, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(payee_id) DO UPDATE SET kind = excluded.kind, note = excluded.note, updated_at = excluded.updated_at`,
		id, kind, note, stamp(s.now())); err != nil {
		return "", fmt.Errorf("save payee label: %w", err)
	}
	s.changed()
	said := fmt.Sprintf("Recorded: payee %s is %s", id, kindWords(kind))
	if note != "" {
		said += fmt.Sprintf(" (the owner's note: %q)", note)
	}
	return said + ". money_recurring and money_upcoming will treat it that way from now on.", nil
}

func kindWords(kind string) string {
	if kind == kindNotRecurring {
		return "not a regular payment"
	}
	if kind == kindIncome {
		return "income"
	}
	return "a " + kind
}

func (s *Service) labelPayeeTool() agent.Tool {
	return agent.Tool{
		Name: "money_label_payee",
		Description: "Record what the owner says a recurring payment is, using the payee id money_recurring shows: bill (a necessary " +
			"payment), subscription (optional), income (money they rely on) or not_recurring (not a regular payment at all). " +
			"'unknown' removes a label. Optionally add the owner's own words for it as a short note, e.g. 'car insurance'. Call it " +
			"only with the owner's answer, never a guess from the payee's name: whether something is a bill is theirs to say " +
			"(they may not pay rent at all). After it, money_recurring and money_upcoming use the label. Changes nothing at the bank.",
		Schema: schema(`{
			"payee":{"type":"string","description":"The payee id from money_recurring, like p1a2b3c4d5e."},
			"kind":{"type":"string","enum":["bill","subscription","income","not_recurring","unknown"],"description":"What the owner said it is. unknown removes the label."},
			"note":{"type":"string","description":"Optional: the owner's own words for it, one line, at most 100 characters."}}`),
		Effect:  agent.EffectLocal,
		Label:   "Noting what a payment is",
		Timeout: toolTimeout,
		Run: s.guarded(func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Payee string `json:"payee"`
				Kind  string `json:"kind"`
				Note  string `json:"note"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			said, err := s.LabelPayee(ctx, a.Payee, a.Kind, a.Note)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: said, Stored: said}, nil
		}),
	}
}
