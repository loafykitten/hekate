package money

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func dateOf(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func rowsOn(dates ...string) []txRow {
	var rows []txRow
	for i, d := range dates {
		rows = append(rows, txRow{id: fmt.Sprint("r", i), date: d, day: dateOf(d), name: "ACME PAYROLL", cents: 240000})
	}
	return rows
}

// Pay on the 1st and 15th, moved to the Friday before when it falls on a weekend.
var semiMonthlyDates = []string{
	"2026-05-01", "2026-05-15", "2026-06-01", "2026-06-15", "2026-07-01", "2026-07-15",
	"2026-07-31", "2026-08-14", "2026-09-01", "2026-09-15", "2026-10-01", "2026-10-15",
}

func TestFitTwiceMonthly(t *testing.T) {
	// Fourteen payments, 14 days apart, ending 2026-10-16.
	var biweekly []string
	for i := 13; i >= 0; i-- {
		biweekly = append(biweekly, dateOf("2026-10-16").AddDate(0, 0, -14*i).Format(time.DateOnly))
	}
	for _, tc := range []struct {
		name     string
		dates    []string
		ok       bool
		a, b     int
		lastNom  string
		nextNom  string
		wantNote string
	}{
		{"1st and 15th with weekend shifts", semiMonthlyDates, true, 1, 15, "2026-10-15", "2026-11-01", ""},
		{"15th and the last day, through February", []string{"2025-12-15", "2025-12-31", "2026-01-15", "2026-01-30", "2026-02-13", "2026-02-27", "2026-03-13", "2026-03-31"},
			true, 15, 31, "2026-03-31", "2026-04-15", ""},
		{"a month with one payment", []string{"2026-05-01", "2026-05-15", "2026-06-01", "2026-07-01", "2026-07-15", "2026-08-01"}, false, 0, 0, "", "", ""},
		{"a month with three", []string{"2026-05-01", "2026-05-15", "2026-05-30", "2026-06-01", "2026-06-15", "2026-07-01"}, false, 0, 0, "", "", ""},
		{"too few payments", []string{"2026-08-01", "2026-08-15", "2026-09-01", "2026-09-15"}, false, 0, 0, "", "", ""},
		{"every two weeks, a year of it", biweekly, false, 0, 0, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, last, ok := fitTwiceMonthly(rowsOn(tc.dates...))
			if ok != tc.ok || a != tc.a || b != tc.b {
				t.Fatalf("fit = %d %d %v, want %d %d %v", a, b, ok, tc.a, tc.b, tc.ok)
			}
			if !ok {
				return
			}
			if last.Format(time.DateOnly) != tc.lastNom || nominalAfter(last, a, b).Format(time.DateOnly) != tc.nextNom {
				t.Errorf("last nominal %v, next %v; want %s then %s", last, nominalAfter(last, a, b), tc.lastNom, tc.nextNom)
			}
		})
	}
}

func TestNominalDatesClampToShortMonths(t *testing.T) {
	for from, want := range map[string]string{"2026-01-31": "2026-02-15", "2026-02-15": "2026-02-28", "2026-02-28": "2026-03-15", "2028-02-15": "2028-02-29", "2026-12-31": "2027-01-15"} {
		if got := nominalAfter(dateOf(from), 15, 31).Format(time.DateOnly); got != want {
			t.Errorf("after %s: %s, want %s", from, got, want)
		}
	}
}

func TestTwiceAMonthAndEveryTwoWeeksSideBySide(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	for i, d := range semiMonthlyDates {
		f.insertOn("acc-1", fmt.Sprint("semi", i), d, "SEMI PAYROLL", 240000, "INCOME")
	}
	f.seriesOn("acc-1", "bi", "2026-10-16", "BIWEEK PAYROLL", 14, 14, 240000, "INCOME")
	f.label("SEMI PAYROLL", true, "income", "")
	f.label("BIWEEK PAYROLL", true, "income", "")

	rec, _ := f.tool("money_recurring", `{}`)
	up, _ := f.tool("money_upcoming", `{}`)
	t.Logf("\n%s\n\n%s", rec.Content, up.Content)
	for _, want := range []string{
		"income about 10000.00 USD (2 items)", // 2 x 2400 + 26/12 x 2400
		"| SEMI PAYROLL | twice a month | account: Spending | typical 2400.00 USD | last 2400.00 USD on 2026-10-15 | next expected about 2026-11-01",
		"| BIWEEK PAYROLL | every two weeks | account: Spending | typical 2400.00 USD | last 2400.00 USD on 2026-10-16 | next expected about 2026-10-30",
	} {
		if !strings.Contains(rec.Content, want) {
			t.Errorf("recurring lacks %q", want)
		}
	}
	for _, want := range []string{
		"income 9600.00 USD.",
		"2026-10-30 | 2400.00 USD | income: BIWEEK PAYROLL",
		"2026-11-01 | 2400.00 USD | income: SEMI PAYROLL",
		"2026-11-13 | 2400.00 USD | income: BIWEEK PAYROLL",
		"2026-11-15 | 2400.00 USD | income: SEMI PAYROLL",
	} {
		if !strings.Contains(up.Content, want) {
			t.Errorf("upcoming lacks %q", want)
		}
	}
	if strings.Contains(up.Content, "2026-10-29") {
		t.Error("the twice-a-month pay was projected on the two-week rhythm")
	}
}

// Plaid replaces a pending transaction with its posted one, so a pending row is
// never a copy of a posted one: a weekly charge pending today is the next one.
func TestPendingChargeIsAnOccurrence(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	f.seriesOn("acc-1", "wk", "2026-10-13", "WEEKLY PASS", 7, 4, -1200, "ENTERTAINMENT")
	f.insert("wk-pending", "2026-10-20", "WEEKLY PASS", "", -1200, "ENTERTAINMENT", true)
	res, _ := f.tool("money_recurring", `{}`)
	if !strings.Contains(res.Content, "| WEEKLY PASS | weekly | money out | account: Spending | typical 12.00 USD | last 12.00 USD on 2026-10-20 | next expected about 2026-10-27") {
		t.Errorf("the pending charge didn't count:\n%s", res.Content)
	}
}

func TestSamePayeeAndAmountOnTwoAccountsStaysTwoPatterns(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	f.seriesOn("acc-1", "a", "2026-10-02", "SHARED SERVICE", 30, 4, -1000, "GENERAL_SERVICES")
	f.seriesOn("acc-2", "b", "2026-10-04", "SHARED SERVICE", 30, 3, -1000, "GENERAL_SERVICES")
	res, _ := f.tool("money_recurring", `{}`)
	if strings.Count(res.Content, "| SHARED SERVICE |") != 2 || !strings.Contains(res.Content, "account: Spending") || !strings.Contains(res.Content, "account: Example Card") {
		t.Errorf("want one line per account:\n%s", res.Content)
	}
}

func retire(f *fixture, account string) {
	f.t.Helper()
	if _, err := f.store.DB().ExecContext(f.t.Context(), `UPDATE money_accounts SET active = 0 WHERE account_id = ?`, account); err != nil {
		f.t.Fatal(err)
	}
}

// Pay moved to a new account: the old account's payroll no longer counts, but its
// history stays in the trends.
func TestRetiredAccountsAreLeftOutOfForecasts(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000) // acc-1 checking (new), acc-3 savings (old)
	f.seriesOn("acc-3", "old", "2026-10-16", "ACME PAYROLL", 14, 6, 240000, "INCOME")
	f.seriesOn("acc-1", "new", "2026-10-16", "ACME PAYROLL", 14, 6, 240000, "INCOME")
	f.insertOn("acc-3", "old-sep", "2026-09-10", "OLD ACCOUNT DEPOSIT", 100000, "INCOME")
	f.label("ACME PAYROLL", true, "income", "")

	both, _ := f.tool("money_recurring", `{}`)
	if strings.Count(both.Content, "| ACME PAYROLL |") != 2 {
		t.Fatalf("before retiring: want two patterns:\n%s", both.Content)
	}
	retire(f, "acc-3")
	res, _ := f.tool("money_recurring", `{}`)
	if strings.Count(res.Content, "| ACME PAYROLL |") != 1 || !strings.Contains(res.Content, "account: Spending") || !strings.Contains(res.Content, "income about 5200.00 USD (1 item)") {
		t.Errorf("after retiring:\n%s", res.Content)
	}
	up, _ := f.tool("money_upcoming", `{}`)
	if !strings.Contains(up.Content, "income 4800.00 USD.") || strings.Contains(up.Content, "on Rainy Day") {
		t.Errorf("the retired account still counts in the forecast:\n%s", up.Content)
	}
	// September holds both accounts' payroll (4 x 2400.00) and the old account's 1000.00 deposit.
	tr, _ := f.tool("money_trends", `{"months":2}`)
	if !strings.Contains(tr.Content, "2026-09: out 0.00 USD | in 10600.00 USD") {
		t.Errorf("trends dropped the retired account's history:\n%s", tr.Content)
	}
}

// A transaction whose account isn't known yet (a sync page that came before the
// accounts refresh) still counts.
func TestTransactionOnAnUnknownAccountStillCounts(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	f.seriesOn("acc-9", "u", "2026-10-02", "NEW BANK SERVICE", 30, 4, -2500, "GENERAL_SERVICES")
	res, _ := f.tool("money_recurring", `{}`)
	if !strings.Contains(res.Content, "| NEW BANK SERVICE | monthly | money out |") {
		t.Errorf("an unknown account's payments were dropped:\n%s", res.Content)
	}
}
