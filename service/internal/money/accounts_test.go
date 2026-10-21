package money

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// insertOn stores a transaction on the given account (acc-1 checking, acc-2 card, acc-3 savings).
func (f *fixture) insertOn(account, id, date, name string, cents int64, category string) {
	f.t.Helper()
	_, err := f.store.DB().ExecContext(f.t.Context(), `
		INSERT INTO money_transactions (transaction_id, item_id, account_id, date, name, merchant, amount_cents, pending, category)
		VALUES (?, 1, ?, ?, ?, '', ?, 0, ?)`, id, account, date, name, cents, category)
	if err != nil {
		f.t.Fatal(err)
	}
}

// seriesOn is series on another account.
func (f *fixture) seriesOn(account, prefix, end, name string, gap, n int, cents int64, category string) {
	f.t.Helper()
	last, err := time.Parse(time.DateOnly, end)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := range n {
		f.insertOn(account, fmt.Sprintf("%s-%d", prefix, i), last.AddDate(0, 0, -gap*(n-1-i)).Format(time.DateOnly), name, cents, category)
	}
}

// Only items on a checking account move the checking balance. A card charge adds
// to what is owed, a deposit into savings isn't spendable cash, and the card
// payment's other side (money in on the card) is not a payday.
func TestUpcomingKeepsAccountsApart(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(470) // acc-1 checking, acc-2 card, acc-3 savings
	f.seriesOn("acc-1", "rent", "2026-09-24", "EXAMPLE PROPERTIES RENT", 30, 3, -120000, "RENT_AND_UTILITIES")
	f.seriesOn("acc-1", "pay", "2026-10-16", "ACME PAYROLL", 14, 4, 240000, "INCOME")
	f.seriesOn("acc-2", "sub", "2026-09-26", "STREAMLY", 30, 3, -1599, "ENTERTAINMENT")
	f.seriesOn("acc-3", "sav", "2026-09-28", "SAVINGS CLUB DEPOSIT", 30, 3, 50000, "INCOME")
	f.seriesOn("acc-1", "cardpay", "2026-09-15", "EXAMPLE CARD PAYMENT", 30, 3, -30000, "LOAN_PAYMENTS")
	f.seriesOn("acc-2", "cardin", "2026-09-15", "EXAMPLE CARD PAYMENT", 30, 3, 30000, "LOAN_PAYMENTS")
	f.label("EXAMPLE PROPERTIES RENT", false, "bill", "")
	f.label("ACME PAYROLL", true, "income", "")
	f.label("STREAMLY", false, "subscription", "")
	f.label("SAVINGS CLUB DEPOSIT", true, "income", "")

	rec, err := f.tool("money_recurring", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(rec.Content, "EXAMPLE CARD PAYMENT"); n != 1 {
		t.Errorf("the card payment is listed %d times, want once (its card side isn't a payday):\n%s", n, rec.Content)
	}
	for _, want := range []string{
		"| EXAMPLE CARD PAYMENT | monthly | money out | account: Spending |",
		"| STREAMLY | monthly | account: Example Card |",
		"| SAVINGS CLUB DEPOSIT | monthly | account: Rainy Day |",
	} {
		if !strings.Contains(rec.Content, want) {
			t.Errorf("recurring lacks %q", want)
		}
	}

	res, err := f.tool("money_upcoming", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", res.Content)
	for _, want := range []string{
		"Checking now: 470.00 USD",
		"Planned around what the owner confirmed on checking (1 bill, 0 subscriptions, 1 income source). Expected in the window: payments out 1200.00 USD, income 4800.00 USD.",
		"Confirmed items on a credit card (1) or in savings or another account (1) are listed apart: they don't move the checking balance.",
		"Lowest point: -730.00 USD on 2026-10-24.",
		"Next payday: 2026-10-30. Confirmed payments from checking expected before then: 1200.00 USD, against 470.00 USD in checking now: short by 730.00 USD.",
		"Unconfirmed payments out before then would add 300.00 USD.",
		"1 payment going out of checking, 600.00 USD in the window. If these also come, the lowest point would be -1030.00 USD on 2026-10-24.",
		"Confirmed by the owner, on checking:\n2026-10-24 | -1200.00 USD | bill: EXAMPLE PROPERTIES RENT | checking after: -730.00 USD\n2026-10-30 | 2400.00 USD | income: ACME PAYROLL | checking after: 1670.00 USD\n",
		"Charged to a card (adds to what you owe; not taken from checking, not in the balances above):\n2026-10-26 | -15.99 USD | subscription: STREAMLY | on Example Card\n",
		"In savings or another account (not part of the checking balance):\n2026-10-28 | 500.00 USD | income: SAVINGS CLUB DEPOSIT | on Rainy Day\n",
		"Not confirmed yet (not in the balances above):\n2026-10-20 | -300.00 USD | unconfirmed: EXAMPLE CARD PAYMENT (expected earlier, not seen yet) | on Spending\n",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("upcoming lacks %q", want)
		}
	}
	for _, not := range []string{"checking after: -745.99", "checking after: 2170", "checking after: 2170.00", "checking after: 1654"} {
		if strings.Contains(res.Content, not) {
			t.Errorf("a card or savings item reached the checking balance: %q", not)
		}
	}
}

// The same payee on two accounts is two patterns with one label.
func TestLabelAppliesToAPayeeOnEveryAccount(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	f.seriesOn("acc-1", "a", "2026-10-02", "SHARED SERVICE", 30, 4, -1000, "GENERAL_SERVICES")
	f.seriesOn("acc-2", "b", "2026-10-05", "SHARED SERVICE", 30, 4, -2000, "GENERAL_SERVICES")
	before, _ := f.tool("money_recurring", `{}`)
	if n := strings.Count(before.Content, "| SHARED SERVICE |"); n != 2 || strings.Count(before.Content, "id "+payeeID("shared service", false)) != 2 {
		t.Errorf("want two lines with one id:\n%s", before.Content)
	}
	f.label("SHARED SERVICE", false, "subscription", "")
	after, _ := f.tool("money_recurring", `{}`)
	_, subs, _ := strings.Cut(after.Content, "Subscriptions (confirmed by the owner):")
	if strings.Count(subs, "| SHARED SERVICE |") != 2 || !strings.Contains(subs, "account: Spending") || !strings.Contains(subs, "account: Example Card") ||
		!strings.Contains(after.Content, "subscriptions about 30.00 USD (2 items)") {
		t.Errorf("the label did not apply to both:\n%s", after.Content)
	}
}

func TestSustainedPriceChange(t *testing.T) {
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	build := func(amounts []int64) []txRow {
		var rows []txRow
		for i, a := range amounts {
			day := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -30*(len(amounts)-1-i))
			rows = append(rows, txRow{id: fmt.Sprint("t", i), date: day.Format(time.DateOnly), day: day, name: "ACME CLOUD", cents: -a})
		}
		return rows
	}
	for _, tc := range []struct {
		name         string
		amounts      []int64
		expected     int64
		changed      bool
		recent       bool
		from, to     int64
		changeAtStep int // index of the amount where the change showed
	}{
		{"moved and stayed", []int64{1000, 1000, 1000, 1200, 1200}, 1200, false, true, 1000, 1200, 3},
		{"moved and stayed, three payments after", []int64{1000, 1000, 1000, 1200, 1200, 1200}, 1200, false, true, 1000, 1200, 3},
		{"just changed", []int64{1000, 1000, 1000, 1000, 1200}, 1200, true, true, 1000, 1200, 4},
		{"a blip that went back", []int64{1000, 1000, 1500, 1000, 1000}, 1000, false, false, 0, 0, 0},
		{"changed long ago", []int64{1000, 1200, 1200, 1200, 1200, 1200}, 1200, false, false, 0, 0, 0},
		{"small drift", []int64{1000, 1030, 1000, 1040}, 1015, false, false, 0, 0, 0},
		{"no change", []int64{1599, 1599, 1599}, 1599, false, false, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := build(tc.amounts)
			items, _ := detectRecurring(rows, today)
			if len(items) != 1 {
				t.Fatalf("items = %+v", items)
			}
			it := items[0]
			if it.expected != tc.expected || it.changed != tc.changed || it.chSeen != tc.recent {
				t.Fatalf("expected %d changed %v recent %v, got %d %v %v", tc.expected, tc.changed, tc.recent, it.expected, it.changed, it.chSeen)
			}
			if tc.recent && (it.chFrom != tc.from || it.chTo != tc.to || it.chDay.Format(time.DateOnly) != rows[tc.changeAtStep].date) {
				t.Errorf("change = %d to %d on %v, want %d to %d on %s", it.chFrom, it.chTo, it.chDay, tc.from, tc.to, rows[tc.changeAtStep].date)
			}
		})
	}
}

func TestPriceChangeIsWordedAndCounted(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	for i, a := range []int64{1000, 1000, 1000, 1200, 1200} {
		d := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -30*(4-i)).Format(time.DateOnly)
		f.insertOn("acc-1", fmt.Sprint("c", i), d, "ACME CLOUD", -a, "GENERAL_SERVICES")
	}
	f.label("ACME CLOUD", false, "subscription", "")
	res, _ := f.tool("money_recurring", `{}`)
	for _, want := range []string{
		"typical 10.00 USD | last 12.00 USD on 2026-10-03 | next expected about 2026-11-03 | changed from 10.00 USD to 12.00 USD on 2026-09-03",
		"subscriptions about 12.00 USD (1 item)",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("lacks %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "CHANGED") {
		t.Error("flagged as just changed")
	}
}

// A yearly payment is seen twice only with about two years of history.
func TestYearlyPaymentDueSoonIsFound(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(2000)
	f.insertOn("acc-1", "y0", "2024-11-10", "EXAMPLE ANNUAL FEE", -9900, "GENERAL_SERVICES")
	f.insertOn("acc-1", "y1", "2025-11-10", "EXAMPLE ANNUAL FEE", -9900, "GENERAL_SERVICES")
	res, err := f.tool("money_recurring", `{}`)
	if err != nil || !strings.Contains(res.Content, "| EXAMPLE ANNUAL FEE | yearly | money out | account: Spending | typical 99.00 USD | last 99.00 USD on 2025-11-10 | next expected about 2026-11-10") {
		t.Fatalf("recurring: %v\n%s", err, res.Content)
	}
	before, _ := f.tool("money_upcoming", `{}`)
	if strings.Contains(before.Content, "EXAMPLE ANNUAL FEE") {
		t.Error("an unconfirmed payment is in the confirmed plan")
	}
	f.label("EXAMPLE ANNUAL FEE", false, "bill", "")
	after, _ := f.tool("money_upcoming", `{}`)
	for _, want := range []string{"2026-11-10 | -99.00 USD | bill: EXAMPLE ANNUAL FEE | checking after: 1901.00 USD", "Lowest point: 1901.00 USD on 2026-11-10."} {
		if !strings.Contains(after.Content, want) {
			t.Errorf("upcoming lacks %q:\n%s", want, after.Content)
		}
	}
}
