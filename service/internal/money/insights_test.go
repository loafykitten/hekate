package money

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

var figure = regexp.MustCompile(`\d+\.\d\d`)

// insert stores a transaction directly. cents: money in (+) or out (-).
func (f *fixture) insert(id, date, name, merchant string, cents int64, category string, pending bool) {
	f.t.Helper()
	_, err := f.store.DB().ExecContext(f.t.Context(), `
		INSERT INTO money_transactions (transaction_id, item_id, account_id, date, name, merchant, amount_cents, pending, category)
		VALUES (?, 1, 'acc-1', ?, ?, ?, ?, ?, ?)`, id, date, name, merchant, cents, pending, category)
	if err != nil {
		f.t.Fatal(err)
	}
}

// series inserts n transactions gap days apart, the last one on end (YYYY-MM-DD).
func (f *fixture) series(prefix, end, name string, gap, n int, cents int64, category string) {
	f.t.Helper()
	last, err := time.Parse(time.DateOnly, end)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := range n {
		d := last.AddDate(0, 0, -gap*(n-1-i)).Format(time.DateOnly)
		f.insert(fmt.Sprintf("%s-%d", prefix, i), d, name, "", cents, category, false)
	}
}

// withAccounts connects a bank whose checking holds available (current is 30 more), plus savings and a card.
func (f *fixture) withAccounts(available float64) {
	f.t.Helper()
	savings := map[string]any{
		"account_id": "acc-3", "name": "Rainy Day", "mask": "5555", "type": "depository", "subtype": "savings",
		"balances": map[string]any{"current": 3000.0, "available": 3000.0, "limit": nil, "iso_currency_code": "USD"},
	}
	chk := checking(available + 30)
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) {
		return 200, map[string]any{"accounts": []any{chk, savings, creditCard("acc-2", 300, 2000)}}
	})
	f.connect()
}

func TestDetectCadences(t *testing.T) {
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	mk := func(name string, gaps []int, cents int64) []txRow {
		// The last row is two days ago; gaps run backwards from it.
		d := today.AddDate(0, 0, -2)
		days := []time.Time{d}
		for i := len(gaps) - 1; i >= 0; i-- {
			d = d.AddDate(0, 0, -gaps[i])
			days = append([]time.Time{d}, days...)
		}
		var rows []txRow
		for i, day := range days {
			rows = append(rows, txRow{id: fmt.Sprint(name, i), date: day.Format(time.DateOnly), day: day, name: name, cents: cents})
		}
		return rows
	}
	for _, tc := range []struct {
		name    string
		gaps    []int
		cents   int64
		want    string // cadence name; "" when not recurring
		wantGap int
	}{
		{"weekly", []int{7, 7, 7}, -500, "weekly", 7},
		{"weekly wobble", []int{6, 8, 7, 7}, -500, "weekly", 7},
		{"biweekly", []int{14, 14, 14}, 240000, "every two weeks", 14},
		{"biweekly 15", []int{15, 14, 16}, 240000, "every two weeks", 15},
		{"monthly", []int{30, 31, 30}, -1599, "monthly", 30},
		{"monthly with a skipped month", []int{30, 61, 31}, -1599, "monthly", 31},
		{"quarterly", []int{90, 91}, -8500, "quarterly", 91},
		{"yearly two charges", []int{360}, -60000, "yearly", 360},
		{"irregular", []int{3, 40, 9, 22}, -1500, "", 0},
		{"two monthly charges only", []int{30}, -1599, "", 0},
		{"two weekly charges only", []int{7}, -500, "", 0},
		{"every day", []int{1, 1, 1, 1}, -400, "", 0},
		{"too spread out", []int{45, 45, 45}, -1000, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, _ := detectRecurring(mk("ACME", tc.gaps, tc.cents), today)
			if tc.want == "" {
				if len(items) != 0 {
					t.Fatalf("detected %+v, want nothing", items[0])
				}
				return
			}
			if len(items) != 1 || items[0].cadence.name != tc.want || items[0].gap != tc.wantGap {
				t.Fatalf("items = %+v", items)
			}
			// Weekly rhythms step by the gap; monthly and longer keep the day of the month.
			wantNext := items[0].lastDay.AddDate(0, 0, tc.wantGap)
			if m := items[0].cadence.months; m > 0 {
				wantNext = items[0].lastDay.AddDate(0, m, 0)
			}
			if !items[0].next.Equal(wantNext) {
				t.Errorf("next = %v, want %v", items[0].next, wantNext)
			}
			if items[0].income != (tc.cents > 0) {
				t.Errorf("income = %v", items[0].income)
			}
		})
	}
}

func TestPayeeKey(t *testing.T) {
	for in, want := range map[string]string{
		"STREAMLY":              "streamly",
		"SQ *Corner Cafe #1234": "sq corner cafe",
		"Corner   Cafe 0042":    "corner cafe",
		"ACME PAYROLL 8812":     "acme payroll",
		"12345":                 "",
	} {
		if got := payeeKey(in); got != want {
			t.Errorf("payeeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// seedRecurring stores a year-ish of habits around the fixture's clock (2026-10-20).
func seedRecurring(f *fixture) {
	// Monthly subscription that went up on its latest charge.
	for i, d := range []string{"2026-06-03", "2026-07-03", "2026-08-03", "2026-09-03"} {
		f.insert(fmt.Sprint("sub", i), d, "STREAMLY 4411", "Streamly", -1299, "ENTERTAINMENT", false)
	}
	f.insert("sub4", "2026-10-03", "STREAMLY 9902", "Streamly", -1599, "ENTERTAINMENT", false)
	f.series("rent", "2026-10-01", "EXAMPLE PROPERTIES RENT", 30, 5, -120000, "RENT_AND_UTILITIES")
	f.series("loan", "2026-10-05", "EXAMPLE AUTO LOAN", 30, 5, -25000, "LOAN_PAYMENTS")
	f.series("pay", "2026-10-16", "ACME PAYROLL 1234", 14, 6, 240000, "INCOME")
	f.series("cafe", "2026-10-13", "CORNER COFFEE CLUB", 7, 5, -500, "FOOD_AND_DRINK")
	f.series("water", "2026-08-01", "EXAMPLE WATER CO", 90, 3, -8500, "RENT_AND_UTILITIES")
	f.insert("ins0", "2025-10-22", "EXAMPLE INSURANCE", "", -60000, "GENERAL_SERVICES", false)
	f.insert("ins1", "2026-10-14", "EXAMPLE INSURANCE", "", -60000, "GENERAL_SERVICES", false)
	// Not recurring: irregular visits, a transfer to savings every month, and a gym that stopped in the spring.
	for i, d := range []string{"2026-07-01", "2026-07-04", "2026-08-13", "2026-08-22", "2026-09-14"} {
		f.insert(fmt.Sprint("diner", i), d, "RANDOM DINER", "", -2300, "FOOD_AND_DRINK", false)
	}
	f.series("move", "2026-10-02", "MOVE TO SAVINGS", 30, 5, -50000, "TRANSFER_OUT")
	f.series("gym", "2026-04-10", "OLD GYM", 30, 4, -3000, "PERSONAL_CARE")
}

func seedMonths(f *fixture) {
	f.insert("j1", "2026-07-09", "JULY LUNCH", "Corner Cafe", -1000, "FOOD_AND_DRINK", false)
	f.insert("a1", "2026-08-05", "CORNER CAFE 12", "Corner Cafe", -4000, "FOOD_AND_DRINK", false)
	f.insert("a2", "2026-08-19", "CORNER CAFE 12", "Corner Cafe", -6000, "FOOD_AND_DRINK", false)
	f.insert("a3", "2026-08-20", "METRO TRANSIT", "Metro", -5000, "TRANSPORTATION", false)
	f.insert("a4", "2026-08-01", "ACME PAYROLL", "", 200000, "INCOME", false)
	f.insert("a5", "2026-08-03", "MOVE TO SAVINGS", "", -50000, "TRANSFER_OUT", false)
	f.insert("s1", "2026-09-05", "CORNER CAFE 12", "Corner Cafe", -8000, "FOOD_AND_DRINK", false)
	f.insert("s2", "2026-09-12", "GREEN MARKET", "", -12000, "FOOD_AND_DRINK", false)
	f.insert("s3", "2026-09-01", "EXAMPLE PROPERTIES RENT", "", -120000, "RENT_AND_UTILITIES", false)
	f.insert("s4", "2026-09-01", "ACME PAYROLL", "", 200000, "INCOME", false)
	f.insert("o1", "2026-10-06", "CORNER CAFE 12", "Corner Cafe", -3000, "FOOD_AND_DRINK", true)
	f.insert("o2", "2026-10-01", "ACME PAYROLL", "", 200000, "INCOME", false)
}

func TestTrendsTool(t *testing.T) {
	f := newFixture(t)
	f.connect()
	seedMonths(f)

	res, err := f.tool("money_trends", `{"months":3,"category":"Food and drink"}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", res.Content)
	for _, want := range []string{
		"Month by month, Food and drink, last 3 months (USD)",
		"Average over the 2 complete months: out 150.00 USD, in 0.00 USD. Highest out: 2026-09 (200.00 USD). Lowest out: 2026-08 (100.00 USD).",
		"2026-08: out 100.00 USD | in 0.00 USD\n",
		"2026-09: out 200.00 USD | in 0.00 USD\n",
		"2026-10 (so far, partial): out 30.00 USD | in 0.00 USD\n",
		"The current month is partial",
		"Corner Cafe | 210.00 USD | 4 transactions",
		"GREEN MARKET | 120.00 USD | 1 transaction\n",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content lacks %q", want)
		}
	}
	if strings.Contains(res.Content, "top:") || strings.Contains(res.Content, "Metro") {
		t.Errorf("filtered view shows other categories:\n%s", res.Content)
	}
	if res.Card != nil {
		t.Errorf("card = %+v", res.Card)
	}
	if figure.MatchString(res.Stored) || strings.Contains(res.Stored, "Corner") || !strings.Contains(res.Stored, "figures not kept") {
		t.Errorf("stored = %q", res.Stored)
	}

	all, _ := f.tool("money_trends", `{"months":3}`)
	t.Logf("\n%s", all.Content)
	for _, want := range []string{
		"2026-08: out 150.00 USD | in 2000.00 USD | transfers and loan payments 500.00 USD | top: Food and drink 100.00, Transportation 50.00\n",
		"2026-09: out 1400.00 USD | in 2000.00 USD | top: Rent and utilities 1200.00, Food and drink 200.00\n",
		"Average over the 2 complete months: out 775.00 USD, in 2000.00 USD. Highest out: 2026-09 (1400.00 USD). Lowest out: 2026-08 (150.00 USD).",
		"EXAMPLE PROPERTIES RENT | 1200.00 USD | 1 transaction\n",
	} {
		if !strings.Contains(all.Content, want) {
			t.Errorf("unfiltered lacks %q", want)
		}
	}
	if strings.Contains(all.Content, "MOVE TO SAVINGS") {
		t.Error("a transfer is listed as a payee")
	}

	// A payee filter, and months before the first stored transaction.
	q, _ := f.tool("money_trends", `{"months":6,"q":"cafe"}`)
	for _, want := range []string{"payees matching \"cafe\"", "2026-05: no data", "2026-06: no data", "2026-07: out 10.00 USD", "Average over the 3 complete months: out 63.33 USD"} {
		if !strings.Contains(q.Content, want) {
			t.Errorf("q view lacks %q:\n%s", want, q.Content)
		}
	}

	none, _ := f.tool("money_trends", `{"category":"Travel"}`)
	if !strings.Contains(none.Content, "No spending found for this filter.") {
		t.Errorf("none:\n%s", none.Content)
	}
	capped, _ := f.tool("money_trends", `{"months":99}`)
	if !strings.Contains(capped.Content, "last 24 months") {
		t.Errorf("months not capped:\n%s", capped.Content)
	}
}

// Bank text in all three new tools stays on one line inside a wrapper whose nonce changes per call.

func TestMonthlyItemsKeepTheirDayOfTheMonth(t *testing.T) {
	loc := time.UTC
	day := func(s string) time.Time { d, _ := time.ParseInLocation(time.DateOnly, s, loc); return d }
	monthly := recurring{cadence: cadences[2], gap: 30, lastDay: day("2027-01-31")}
	var got []string
	for d := monthly.step(monthly.lastDay); len(got) < 3; d = monthly.step(d) {
		got = append(got, d.Format(time.DateOnly))
	}
	if strings.Join(got, " ") != "2027-02-28 2027-03-31 2027-04-30" {
		t.Errorf("a bill on the 31st steps to %v", got)
	}
	rent := recurring{cadence: cadences[2], gap: 31, lastDay: day("2026-10-01")}
	if next := rent.step(rent.lastDay).Format(time.DateOnly); next != "2026-11-01" {
		t.Errorf("rent on the 1st next comes %s", next)
	}
	weekly := recurring{cadence: cadences[0], gap: 7, lastDay: day("2026-10-20")}
	if next := weekly.step(weekly.lastDay).Format(time.DateOnly); next != "2026-10-27" {
		t.Errorf("a weekly charge next comes %s", next)
	}
}
