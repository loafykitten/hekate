package money

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
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
	kind       string
	note       string
	stopped    bool
	stoppedOn  string // YYYY-MM-DD it ended or ends, or "" for already
	minC, maxC int64  // the amount (or range) they expect, cents; 0 when not given
}

// LabelInput is everything the owner can say about a payee at once. Saving
// replaces the whole label, so anything that should stay is repeated.
type LabelInput struct {
	ID, Kind, Note string
	Stopped        bool
	StoppedOn      string  // optional YYYY-MM-DD; implies Stopped
	ExpectedMin    float64 // with only one of the two, that is the expected amount
	ExpectedMax    float64
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

// payeeIDOf is the id of the payee a bank's name and merchant text describe, or
// "" when nothing is left of it after normalizing.
func payeeIDOf(name, merchant string, cents int64) string {
	k := payeeKey(txRow{name: name, merchant: merchant}.payee())
	if k == "" {
		return ""
	}
	return payeeID(k, cents > 0)
}

// labels loads every label the owner has given.
func (s *Service) labels(ctx context.Context) (map[string]payeeLabel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT payee_id, kind, note, stopped, stopped_on, COALESCE(expected_min_cents, 0), COALESCE(expected_max_cents, 0)
		FROM money_payees`)
	if err != nil {
		return nil, fmt.Errorf("read payee labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]payeeLabel{}
	for rows.Next() {
		var id string
		var l payeeLabel
		if err := rows.Scan(&id, &l.kind, &l.note, &l.stopped, &l.stoppedOn, &l.minC, &l.maxC); err != nil {
			return nil, fmt.Errorf("read a payee label: %w", err)
		}
		out[id] = l
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// payeeInfo is what the stored transactions show about one payee.
type payeeInfo struct {
	income   bool
	name     string // the bank's text, latest spelling
	last     string // date of the latest transaction
	typical  int64  // median amount, cents (positive)
	count    int
	currency string
}

// seenPayees describes every payee in the stored transactions, by id.
func (s *Service) seenPayees(ctx context.Context) (map[string]payeeInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, merchant, amount_cents, date, currency FROM money_transactions ORDER BY date, transaction_id`)
	if err != nil {
		return nil, fmt.Errorf("read payees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]payeeInfo{}
	amounts := map[string][]int64{}
	for rows.Next() {
		var name, merchant, date, currency string
		var cents int64
		if err := rows.Scan(&name, &merchant, &cents, &date, &currency); err != nil {
			return nil, fmt.Errorf("read a payee: %w", err)
		}
		id := payeeIDOf(name, merchant, cents)
		if id == "" {
			continue
		}
		info := out[id]
		info.income, info.name, info.last, info.currency = cents > 0, txRow{name: name, merchant: merchant}.payee(), date, currency
		info.count++
		out[id] = info
		amounts[id] = append(amounts[id], abs64(cents))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read payees: %w", err)
	}
	for id, v := range amounts {
		info := out[id]
		info.typical = medianInt64(v)
		out[id] = info
	}
	return out, nil
}

// LabelPayee records the owner's answer about a payee, or with kind "unknown"
// removes it. It says what it did, in words that carry no bank text.
func (s *Service) LabelPayee(ctx context.Context, id, kind, note string) (string, error) {
	said, _, err := s.LabelPayeeWith(ctx, LabelInput{ID: id, Kind: kind, Note: note})
	return said, err
}

func toCents(v float64) int64 { return int64(math.Round(v * 100)) }

// LabelPayeeWith records the owner's answer about any payee in the stored
// transactions (by the id money_recurring or money_transactions shows). said
// carries no bank text; detail names the payee (the bank's text, so untrusted)
// with its direction, last-seen date and typical amount, so the caller can see
// which stream it labeled.
func (s *Service) LabelPayeeWith(ctx context.Context, in LabelInput) (said, detail string, err error) {
	id, kind := strings.TrimSpace(in.ID), strings.ToLower(strings.TrimSpace(in.Kind))
	note := oneLine(in.Note, 10*maxNoteLen)
	switch kind {
	case kindBill, kindSubscription, kindIncome, kindNotRecurring, kindUnknown:
	default:
		return "", "", httpx.BadRequest("kind must be bill, subscription, income, not_recurring or unknown")
	}
	if utf8.RuneCountInString(note) > maxNoteLen {
		return "", "", httpx.BadRequest("note is too long: keep it to %d characters", maxNoteLen)
	}
	stoppedOn := strings.TrimSpace(in.StoppedOn)
	if stoppedOn != "" && !validDate(stoppedOn) {
		return "", "", httpx.BadRequest("stopped_on must be a date like 2026-10-01")
	}
	stopped := in.Stopped || stoppedOn != ""
	lo, hi := toCents(in.ExpectedMin), toCents(in.ExpectedMax)
	switch {
	case in.ExpectedMin < 0 || in.ExpectedMax < 0:
		return "", "", httpx.BadRequest("the expected amount can't be negative: give it as a positive figure")
	case lo == 0 && hi != 0:
		lo = hi
	case hi == 0 && lo != 0:
		hi = lo
	case lo > hi:
		return "", "", httpx.BadRequest("expected_min can't be more than expected_max")
	}
	seen, err := s.seenPayees(ctx)
	if err != nil {
		return "", "", err
	}
	info, ok := seen[id]
	if !ok {
		return "", "", httpx.NotFound("no payee has the id %q: find it with money_transactions (every result shows its payee id) or money_recurring", oneLine(id, 20))
	}
	switch {
	case kind == kindIncome && !info.income:
		return "", "", httpx.BadRequest("that payee is money going out, so it can't be income")
	case (kind == kindBill || kind == kindSubscription) && info.income:
		return "", "", httpx.BadRequest("that payee is money coming in, so it can't be a %s", kind)
	}
	dir := "money out"
	if info.income {
		dir = "money in"
	}
	detail = fmt.Sprintf("Payee %s | %s | %s | last seen %s | typical %s | %s", id, oneLine(info.name, 80), dir, info.last,
		centsText(info.typical, info.currency), plural(info.count, "transaction", "transactions"))
	if kind == kindUnknown {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM money_payees WHERE payee_id = ?`, id); err != nil {
			return "", "", fmt.Errorf("clear payee label: %w", err)
		}
		s.changed()
		return fmt.Sprintf("Cleared the label on payee %s: it counts as unconfirmed again.", id), detail, nil
	}
	var minArg, maxArg any
	if lo > 0 {
		minArg, maxArg = lo, hi
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO money_payees (payee_id, kind, note, stopped, stopped_on, expected_min_cents, expected_max_cents, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(payee_id) DO UPDATE SET kind = excluded.kind, note = excluded.note, stopped = excluded.stopped,
			stopped_on = excluded.stopped_on, expected_min_cents = excluded.expected_min_cents,
			expected_max_cents = excluded.expected_max_cents, updated_at = excluded.updated_at`,
		id, kind, note, stopped, stoppedOn, minArg, maxArg, stamp(s.now())); err != nil {
		return "", "", fmt.Errorf("save payee label: %w", err)
	}
	s.changed()
	said = fmt.Sprintf("Recorded: payee %s is %s", id, kindWords(kind))
	if note != "" {
		said += fmt.Sprintf(" (the owner's note: %q)", note)
	}
	switch {
	case stopped && stoppedOn != "":
		said += fmt.Sprintf("; it stopped on %s", stoppedOn)
	case stopped:
		said += "; it has stopped"
	}
	if lo > 0 {
		cur := info.currency
		if lo == hi {
			said += fmt.Sprintf("; the owner expects %s each time", centsText(lo, cur))
		} else {
			said += fmt.Sprintf("; the owner expects %s to %s each time", centsText(lo, cur), centsText(hi, cur))
		}
	}
	return said + ". money_recurring and money_upcoming will treat it that way from now on.", detail, nil
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
		Description: "Record what the owner says a payee is, using its payee id: bill (a necessary payment), subscription " +
			"(optional), income (money they rely on) or not_recurring (not a regular payment at all). 'unknown' removes a label. " +
			"Any payee in the stored transactions works, including one that stopped recurring: ids are shown by money_recurring " +
			"and on every money_transactions result (search by name with its q to find one). Optionally add the owner's own words " +
			"as a short note (e.g. 'car insurance'); stopped (with stopped_on, the date it ended, if they say) when the payment or " +
			"deposit has ended, so forecasts leave it out; and expected_amount, or expected_min and expected_max for a range, in " +
			"dollars, when the latest payment isn't what to expect next. Each call replaces the payee's whole label, so repeat " +
			"what should stay. The result names the payee, its direction, last-seen date and typical amount: check it is the " +
			"stream the owner meant (a deposit can be a mix of several streams), and confirm by name with them when unsure. Call " +
			"it only with the owner's answer, never a guess from the payee's name: whether something is a bill is theirs to say " +
			"(they may not pay rent at all). After it, money_recurring and money_upcoming use the label. Changes nothing at the bank. " +
			untrustedNote,
		Schema: schema(`{
			"payee":{"type":"string","description":"The payee id from money_recurring or money_transactions, like p1a2b3c4d5e."},
			"kind":{"type":"string","enum":["bill","subscription","income","not_recurring","unknown"],"description":"What the owner said it is. unknown removes the label."},
			"note":{"type":"string","description":"Optional: the owner's own words for it, one line, at most 100 characters."},
			"stopped":{"type":"boolean","description":"True when the owner says it has ended; forecasts then leave it out."},
			"stopped_on":{"type":"string","description":"Optional: the date it ended (or ends), YYYY-MM-DD. Implies stopped."},
			"expected_amount":{"type":"number","description":"Optional: what the owner expects each time, in dollars, positive."},
			"expected_min":{"type":"number","description":"Optional: low end of the range they expect, dollars, positive."},
			"expected_max":{"type":"number","description":"Optional: high end of that range."}}`),
		Effect:  agent.EffectLocal,
		Label:   "Noting what a payment is",
		Timeout: toolTimeout,
		Run: s.guarded(func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Payee          string  `json:"payee"`
				Kind           string  `json:"kind"`
				Note           string  `json:"note"`
				Stopped        bool    `json:"stopped"`
				StoppedOn      string  `json:"stopped_on"`
				ExpectedAmount float64 `json:"expected_amount"`
				ExpectedMin    float64 `json:"expected_min"`
				ExpectedMax    float64 `json:"expected_max"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ExpectedAmount != 0 {
				if a.ExpectedMin != 0 || a.ExpectedMax != 0 {
					return agent.Result{}, httpx.BadRequest("give expected_amount, or expected_min and expected_max, not both")
				}
				a.ExpectedMin, a.ExpectedMax = a.ExpectedAmount, a.ExpectedAmount
			}
			said, detail, err := s.LabelPayeeWith(ctx, LabelInput{
				ID: a.Payee, Kind: a.Kind, Note: a.Note, Stopped: a.Stopped, StoppedOn: a.StoppedOn,
				ExpectedMin: a.ExpectedMin, ExpectedMax: a.ExpectedMax,
			})
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: said + "\nThe payee this applies to:\n" + fenced([]string{detail}), Stored: said}, nil
		}),
	}
}
