package money

import (
	"strings"
	"testing"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// The result names the payee it labeled, so a stream mixed up with another is visible.
func TestLabelPayeeEchoesThePayee(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	seedRecurring(f)
	id := payeeID("acme payroll", true)
	res, err := f.toolWith("money_label_payee", `{"payee":"`+id+`","kind":"income"}`, agent.EffectLocal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ACME PAYROLL 1234", "money in", "last seen 2026-10-16", "typical 2400.00 USD", "6 transactions", "<untrusted_bank"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result lacks %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(strings.ToLower(res.Stored), "acme") || strings.Contains(res.Stored, "2400") {
		t.Errorf("stored keeps bank text or figures: %q", res.Stored)
	}
}

func TestLabelPayeeStoppedAndExpected(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	seedRecurring(f)
	ctx := t.Context()
	id := payeeID("streamly", false)

	for name, tc := range map[string]LabelInput{
		"bad date":       {ID: id, Kind: "subscription", StoppedOn: "last week"},
		"negative":       {ID: id, Kind: "subscription", ExpectedMin: -1},
		"inverted range": {ID: id, Kind: "subscription", ExpectedMin: 20, ExpectedMax: 10},
	} {
		if _, _, err := f.svc.LabelPayeeWith(ctx, tc); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	said, _, err := f.svc.LabelPayeeWith(ctx, LabelInput{ID: id, Kind: "subscription", ExpectedMin: 12.99, ExpectedMax: 15.99, StoppedOn: "2026-12-01"})
	if err != nil || !strings.Contains(said, "stopped on 2026-12-01") || !strings.Contains(said, "12.99 USD to 15.99 USD") {
		t.Errorf("said = %q, %v", said, err)
	}
	got, _ := f.svc.labels(ctx)
	if l := got[id]; !l.stopped || l.stoppedOn != "2026-12-01" || l.minC != 1299 || l.maxC != 1599 {
		t.Errorf("label = %+v", l)
	}
	// A single figure is both ends; a new call replaces the whole label.
	if _, _, err := f.svc.LabelPayeeWith(ctx, LabelInput{ID: id, Kind: "subscription", ExpectedMax: 10}); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.svc.labels(ctx); l[id].stopped || l[id].minC != 1000 || l[id].maxC != 1000 {
		t.Errorf("replaced label = %+v", l[id])
	}
}

func TestRecurringUsesExpectedAmountAndSkipsStopped(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	seedRecurring(f)
	ctx := t.Context()
	f.label("Streamly", false, "subscription", "")
	f.label("ACME PAYROLL 1234", true, "income", "")

	before, _ := f.tool("money_recurring", `{}`)
	if !strings.Contains(before.Content, "subscriptions about 15.99 USD (1 item)") {
		t.Fatalf("baseline:\n%s", before.Content)
	}

	// Expected amount replaces the latest (anomalous) payment.
	if _, _, err := f.svc.LabelPayeeWith(ctx, LabelInput{ID: payeeID("streamly", false), Kind: "subscription", ExpectedMax: 12.99}); err != nil {
		t.Fatal(err)
	}
	// The deposit ended.
	if _, _, err := f.svc.LabelPayeeWith(ctx, LabelInput{ID: payeeID("acme payroll", true), Kind: "income", Stopped: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := f.tool("money_recurring", `{}`)
	for _, want := range []string{"subscriptions about 12.99 USD (1 item)", "the owner expects 12.99 USD", "income about 0.00 USD (0 items)",
		"Stopped (the owner said these have ended", "STOPPED (the owner says)"} {
		if !strings.Contains(after.Content, want) {
			t.Errorf("recurring lacks %q:\n%s", want, after.Content)
		}
	}
	up, _ := f.tool("money_upcoming", `{}`)
	if strings.Contains(up.Content, "ACME PAYROLL") || !strings.Contains(up.Content, "have stopped") {
		t.Errorf("upcoming still forecasts the stopped deposit:\n%s", up.Content)
	}
	if !strings.Contains(up.Content, "12.99 USD") || strings.Contains(up.Content, "15.99 USD") {
		t.Errorf("upcoming ignores the expected amount:\n%s", up.Content)
	}
}

// A stop date in the future keeps the item until then.
func TestFutureStopDateCutsTheForecast(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	seedRecurring(f)
	f.label("Streamly", false, "subscription", "")
	if _, _, err := f.svc.LabelPayeeWith(t.Context(), LabelInput{ID: payeeID("streamly", false), Kind: "subscription", StoppedOn: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := f.tool("money_recurring", `{}`); !strings.Contains(res.Content, "STOPPED (the owner says, as of 2026-10-01)") {
		t.Errorf("past stop date:\n%s", res.Content)
	}
	if _, _, err := f.svc.LabelPayeeWith(t.Context(), LabelInput{ID: payeeID("streamly", false), Kind: "subscription", StoppedOn: "2026-10-30"}); err != nil {
		t.Fatal(err)
	}
	res, _ := f.tool("money_recurring", `{}`)
	if !strings.Contains(res.Content, "ends 2026-10-30") || strings.Contains(res.Content, "STOPPED") {
		t.Errorf("future stop date:\n%s", res.Content)
	}
	items, _ := f.svc.recurringReport(t.Context())
	for _, it := range items.items {
		if it.id == payeeID("streamly", false) {
			// next is 2026-11-03, after the stop date: nothing projected.
			if occ := project([]recurring{it}, items.today, items.today.AddDate(0, 0, 30)); len(occ) != 0 {
				t.Errorf("projected past the stop date: %+v", occ)
			}
			return
		}
	}
	t.Error("streamly missing from items")
}

// Any payee in the stored transactions can be found by id and labeled, including one that stopped recurring.
func TestTransactionsCarryPayeeIDs(t *testing.T) {
	f := newFixture(t)
	f.withAccounts(1000)
	seedRecurring(f)
	gym := payeeID("old gym", false)

	res, err := f.tool("money_transactions", `{"q":"old gym"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "Payee id: "+gym) {
		t.Errorf("live result lacks the payee id:\n%s", res.Content)
	}
	if !strings.Contains(res.Stored, gym) || strings.Contains(strings.ToLower(res.Stored), "gym") || figure.MatchString(res.Stored) || !strings.Contains(res.Stored, "figures not kept") {
		t.Errorf("stored = %q", res.Stored)
	}
	if !strings.Contains(res.Card.Data.String(), `"payee_id":"`+gym+`"`) {
		t.Errorf("card lacks the payee id: %s", res.Card.Data)
	}
	if _, err := f.toolWith("money_label_payee", `{"payee":"`+gym+`","kind":"subscription","stopped":true}`, agent.EffectLocal); err != nil {
		t.Errorf("a stopped payee couldn't be labeled: %v", err)
	}
	for _, tl := range f.svc.Tools() {
		if tl.Name == "money_transactions" && !strings.Contains(tl.Description, "payee's id") {
			t.Errorf("description doesn't mention payee ids: %s", tl.Description)
		}
	}
}

// months means that many complete months before the current one; the average and
// the top payees cover exactly those, and the current month stands apart.
func TestTrendsCompleteMonths(t *testing.T) {
	f := newFixture(t)
	f.connect()
	for _, m := range []string{"2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"} {
		f.insert("early-"+m, m+"-01", "SEED", "", 100, "INCOME", false) // history starts on the 1st of April
		f.insert("m-"+m, m+"-10", "BAKERY ONE", "", -1000, "FOOD_AND_DRINK", false)
	}
	f.insert("cur", "2026-10-05", "BAKERY TWO", "", -99000, "FOOD_AND_DRINK", false)
	res, err := f.tool("money_trends", `{"months":6}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"the last 6 complete months: 2026-04-01 to 2026-09-30",
		"Average over the 6 complete months (total out 60.00 USD, total in 6.00 USD, divided by 6): out 10.00 USD, in 1.00 USD",
		"Current month, separate from the average and the top payees",
		"2026-10 (so far, partial): out 990.00 USD",
		"Top payees by money out over the same complete months, 2026-04-01 to 2026-09-30",
		"BAKERY ONE | 60.00 USD | 6 transactions",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("trends lack %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "BAKERY TWO") || strings.Contains(res.Content, "Only ") {
		t.Errorf("the current month leaked into the payees, or a month was dropped:\n%s", res.Content)
	}
}
