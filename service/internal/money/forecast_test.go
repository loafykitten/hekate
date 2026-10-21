package money

import (
	"encoding/json"
	"strings"
	"testing"
)

// checkingAccount is a Plaid checking account; available may be nil (the bank gave none).
func checkingAccount(id, name string, current float64, available *float64) map[string]any {
	var avail any
	if available != nil {
		avail = *available
	}
	return map[string]any{
		"account_id": id, "name": name, "mask": "0001", "type": "depository", "subtype": "checking",
		"balances": map[string]any{"current": current, "available": avail, "limit": nil, "iso_currency_code": "USD"},
	}
}

func fp(v float64) *float64 { return &v }

// connectWith connects a bank whose accounts are the given ones plus a card.
func (f *fixture) connectWith(accounts ...map[string]any) {
	f.t.Helper()
	list := []any{creditCard("acc-2", 300, 2000)}
	for _, a := range accounts {
		list = append(list, a)
	}
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) { return 200, map[string]any{"accounts": list} })
	f.connect()
}

// $100 in one checking account and $2,000 in another: a bill out of the first
// can't be covered by the second.
func TestUpcomingRunsEachCheckingAccountAgainstItsOwnBalance(t *testing.T) {
	f := newFixture(t)
	f.connectWith(checkingAccount("acc-1", "Spending", 130, fp(100)), checkingAccount("acc-4", "Reserve", 2030, fp(2000)))
	f.seriesOn("acc-1", "rent", "2026-09-21", "EXAMPLE PROPERTIES RENT", 30, 3, -50000, "RENT_AND_UTILITIES")
	f.seriesOn("acc-4", "fee", "2026-09-25", "RESERVE FEE", 30, 3, -5000, "BANK_FEES")
	f.seriesOn("acc-4", "pay", "2026-10-16", "ACME PAYROLL", 14, 4, 240000, "INCOME")
	f.label("EXAMPLE PROPERTIES RENT", false, "bill", "")
	f.label("RESERVE FEE", false, "bill", "")
	f.label("ACME PAYROLL", true, "income", "")
	res, err := f.tool("money_upcoming", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", res.Content)
	for _, want := range []string{
		"Checking now: 2 accounts, 2100.00 USD combined (available balances).",
		"WARNING: 1 of 2 checking accounts would dip below zero on these estimates unless money is moved in from another account.",
		"Next payday: 2026-10-30.",
		"The next payday lands in Reserve.",
		"Checking account Spending, now 100.00 USD:\n2026-10-21 | -500.00 USD | bill: EXAMPLE PROPERTIES RENT | checking after: -400.00 USD\n",
		"Lowest point of Spending: -400.00 USD on 2026-10-21.",
		"WARNING: Spending would dip below zero on 2026-10-21 (lowest -400.00 USD) unless money is moved in from another account.",
		"Before the next payday: confirmed payments out of Spending 500.00 USD, against 100.00 USD in it now: short by 400.00 USD.",
		"Checking account Reserve, now 2000.00 USD:\n2026-10-25 | -50.00 USD | bill: RESERVE FEE | checking after: 1950.00 USD\n2026-10-30 | 2400.00 USD | income: ACME PAYROLL | checking after: 4350.00 USD\n",
		"Before the next payday: confirmed payments out of Reserve 50.00 USD, against 2000.00 USD in it now: leaving 1950.00 USD.",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("lacks %q", want)
		}
	}
	if strings.Contains(res.Content, "WARNING: Reserve") || strings.Contains(res.Content, "stays above zero through the window") {
		t.Error("warned about the wrong account, or said all is well")
	}
	// Bank text (account names) appears only inside the wrapper.
	if outside, _, _ := strings.Cut(res.Content, "<untrusted_bank"); strings.Contains(outside, "Spending") || strings.Contains(outside, "Reserve") {
		t.Errorf("an account name is outside the wrapper:\n%s", outside)
	}
}

func TestAnOverdrawnAccountIsWarnedAboutEvenWhenAnotherIsPositive(t *testing.T) {
	f := newFixture(t)
	f.connectWith(checkingAccount("acc-1", "Spending", -20, fp(-50)), checkingAccount("acc-4", "Reserve", 2000, fp(2000)))
	f.seriesOn("acc-4", "pay", "2026-10-16", "ACME PAYROLL", 14, 4, 240000, "INCOME")
	f.label("ACME PAYROLL", true, "income", "")
	res, _ := f.tool("money_upcoming", `{}`)
	for _, want := range []string{
		"WARNING: 1 of 2 checking accounts are already below zero now, before anything ahead (details below).",
		"WARNING: Spending is already below zero now and goes as low as -50.00 USD unless money is moved in from another account.",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("lacks %q:\n%s", want, res.Content)
		}
	}
}

// With no available balance the bank's current figure leaves out what is pending,
// so a rent pending today is taken off it.
func TestPendingPaymentsAdjustACurrentBalance(t *testing.T) {
	f := newFixture(t)
	f.connectWith(checkingAccount("acc-1", "Spending", 1000, nil))
	f.seriesOn("acc-1", "rent", "2026-09-20", "EXAMPLE PROPERTIES RENT", 30, 3, -120000, "RENT_AND_UTILITIES")
	f.insert("rent-pending", "2026-10-20", "EXAMPLE PROPERTIES RENT", "", -120000, "RENT_AND_UTILITIES", true)
	f.insert("pay-pending", "2026-10-19", "ACME PAYROLL", "", 30000, "INCOME", true)
	f.label("EXAMPLE PROPERTIES RENT", false, "bill", "")
	res, _ := f.tool("money_upcoming", `{}`)
	t.Logf("\n%s", res.Content)
	if !strings.Contains(res.Content, "Checking now: 100.00 USD (an estimate: the bank gave no available balance, so this is the current balance adjusted for pending payments and deposits).") {
		t.Errorf("the balance isn't the adjusted estimate:\n%s", res.Content)
	}
	// 1000.00 current - 1200.00 pending rent + 300.00 pending deposit = 100.00.

	// Without pending activity the current balance stands, still marked as an estimate.
	g := newFixture(t)
	g.connectWith(checkingAccount("acc-1", "Spending", 1000, nil))
	res, _ = g.tool("money_upcoming", `{}`)
	if !strings.Contains(res.Content, "Checking now: 1000.00 USD (an estimate:") {
		t.Errorf("fallback without pending:\n%s", res.Content)
	}
	// With an available balance, pending is not subtracted again.
	h := newFixture(t)
	h.connectWith(checkingAccount("acc-1", "Spending", 1000, fp(700)))
	h.insert("p", "2026-10-20", "SOMETHING", "", -30000, "GENERAL_MERCHANDISE", true)
	res, _ = h.tool("money_upcoming", `{}`)
	if !strings.Contains(res.Content, "Checking now: 700.00 USD (available balances).") {
		t.Errorf("available balance changed:\n%s", res.Content)
	}
}

// Payments on one day are summed to judge the pattern, but the card shows a real transaction.
func TestSameDayPaymentsShowARealTransactionOnTheCard(t *testing.T) {
	f := newFixture(t)
	f.connectWith(checkingAccount("acc-1", "Spending", 1000, fp(1000)))
	f.seriesOn("acc-1", "m", "2026-09-03", "SPLIT SERVICE", 30, 3, -1500, "GENERAL_SERVICES")
	f.insertOn("acc-1", "s-a", "2026-10-03", "SPLIT SERVICE", -1000, "GENERAL_SERVICES")
	f.insertOn("acc-1", "s-b", "2026-10-03", "SPLIT SERVICE", -500, "GENERAL_SERVICES")
	res, _ := f.tool("money_recurring", `{}`)
	if !strings.Contains(res.Content, "| SPLIT SERVICE | monthly | money out | account: Spending | typical 15.00 USD | last 15.00 USD on 2026-10-03") {
		t.Fatalf("the two payments weren't judged as one:\n%s", res.Content)
	}
	if res.Card == nil {
		t.Fatal("no card")
	}
	var data struct{ Items []Transaction }
	if err := json.Unmarshal(res.Card.Data, &data); err != nil || len(data.Items) != 1 {
		t.Fatalf("card = %s, %v", res.Card.Data, err)
	}
	if it := data.Items[0]; (it.ID != "s-a" && it.ID != "s-b") || (it.Amount != -10 && it.Amount != -5) {
		t.Errorf("the card holds a synthetic row: %+v", it)
	}
	if it := data.Items[0]; it.ID != "s-b" || it.Amount != -5 {
		t.Errorf("want the latest original row (s-b, -5.00), got %+v", it)
	}
}

// A paycheck that is late may not come: it isn't credited to the forecast, and
// bills are weighed against the next one that is on time.
func TestLateIncomeIsNotCountedUntilItArrives(t *testing.T) {
	f := newFixture(t)
	f.connectWith(checkingAccount("acc-1", "Spending", 470, fp(470)))
	f.seriesOn("acc-1", "pay", "2026-10-02", "ACME PAYROLL", 14, 4, 240000, "INCOME") // Oct 16 hasn't come
	f.seriesOn("acc-1", "rent", "2026-09-24", "EXAMPLE PROPERTIES RENT", 30, 3, -120000, "RENT_AND_UTILITIES")
	f.label("ACME PAYROLL", true, "income", "")
	f.label("EXAMPLE PROPERTIES RENT", false, "bill", "")
	res, _ := f.tool("money_upcoming", `{}`)
	for _, want := range []string{
		"not counted until it arrives",
		"dips below zero",
		"Next payday: 2026-10-30",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("lacks %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "stays above zero") {
		t.Errorf("the late paycheck covered the rent:\n%s", res.Content)
	}
}
