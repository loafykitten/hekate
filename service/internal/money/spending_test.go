package money

import "testing"

func TestCategoryLabel(t *testing.T) {
	for in, want := range map[string]string{
		"FOOD_AND_DRINK": "Food and drink", "INCOME": "Income", "TRANSFER_IN": "Transfers in", "TRANSFER_OUT": "Transfers out",
		"LOAN_PAYMENTS": "Loan payments", "BANK_FEES": "Bank fees", "ENTERTAINMENT": "Entertainment",
		"GENERAL_MERCHANDISE": "General merchandise", "HOME_IMPROVEMENT": "Home improvement", "MEDICAL": "Medical",
		"PERSONAL_CARE": "Personal care", "GENERAL_SERVICES": "General services",
		"GOVERNMENT_AND_NON_PROFIT": "Government and non-profit", "TRANSPORTATION": "Transportation", "TRAVEL": "Travel",
		"RENT_AND_UTILITIES": "Rent and utilities",
		// Not in the table: humanized the same way.
		"PET_SUPPLIES": "Pet supplies", "HOBBIES": "Hobbies", "": "Other",
	} {
		if got := CategoryLabel(in); got != want {
			t.Errorf("CategoryLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeCategory(t *testing.T) {
	for in, want := range map[string]string{
		"food_and_drink": "FOOD_AND_DRINK", "Food and drink": "FOOD_AND_DRINK", " travel ": "TRAVEL", "Rent-and-utilities": "RENT_AND_UTILITIES",
	} {
		if got := normalizeCategory(in); got != want {
			t.Errorf("normalizeCategory(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	this := []flow{
		{"FOOD_AND_DRINK", -1000}, {"FOOD_AND_DRINK", -2500}, {"TRANSPORTATION", -4000},
		{"TRANSFER_OUT", -50000},  // to the owner's own savings: not spending
		{"LOAN_PAYMENTS", -12000}, // paying a card: not spending
		{"INCOME", 240000},        // received
		{"TRANSFER_IN", 50000},    // not received
		{"FOOD_AND_DRINK", 500},   // a refund counts as money in
		{"", -300},                // no category
		{"TRANSFER_OUT", 700},     // money in, whatever its category: received
	}
	before := []flow{{"FOOD_AND_DRINK", -2000}, {"TRAVEL", -9900}, {"TRANSFER_OUT", -100}}
	got := summarize("2026-10", "USD", this, before)
	if got.Spent != 78.0 || got.Received != 2412.0 || got.Transfers != 620.0 || got.PreviousSpent != 119.0 || got.Month != "2026-10" || got.Currency != "USD" {
		t.Errorf("totals = %+v", got)
	}
	want := []SpendingCategory{
		{"TRANSPORTATION", "Transportation", 40, 1, 0},
		{"FOOD_AND_DRINK", "Food and drink", 35, 2, 20},
		{"OTHER", "Other", 3, 1, 0},
	}
	if len(got.Categories) != len(want) {
		t.Fatalf("categories = %+v", got.Categories)
	}
	for i, w := range want {
		if got.Categories[i] != w {
			t.Errorf("category %d = %+v, want %+v", i, got.Categories[i], w)
		}
	}
	if empty := summarize("2026-10", "USD", nil, nil); empty.Categories == nil || len(empty.Categories) != 0 || empty.Spent != 0 {
		t.Errorf("empty = %+v", empty)
	}
}

func TestSpendingMonthBoundariesAndCurrency(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{
			txn("a", "2026-09-30", "LAST OF SEPT", "", 10, "TRAVEL", false),
			txn("b", "2026-10-01", "FIRST OF OCT", "", 20, "TRAVEL", false),
			txn("c", "2026-10-31", "LAST OF OCT", "", 40, "TRAVEL", false),
			txn("d", "2026-11-01", "FIRST OF NOV", "", 80, "TRAVEL", false),
			txn("e", "2026-08-31", "TOO OLD", "", 160, "TRAVEL", false),
			func() map[string]any {
				m := txn("f", "2026-10-10", "EURO LUNCH", "", 500, "FOOD_AND_DRINK", false)
				m["iso_currency_code"] = "EUR"
				return m
			}(),
		}, nil, nil, "c1", false),
	})
	f.connect()
	sp, err := f.svc.Spending(t.Context(), "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	// Only the accounts' currency (USD) counts; the euro purchase is left out.
	if sp.Spent != 60 || sp.PreviousSpent != 10 || len(sp.Categories) != 1 || sp.Categories[0].Previous != 10 {
		t.Errorf("spending = %+v", sp)
	}
	nov, _ := f.svc.Spending(t.Context(), "2026-11")
	if nov.Spent != 80 || nov.PreviousSpent != 60 {
		t.Errorf("november = %+v", nov)
	}
	if def, _ := f.svc.Spending(t.Context(), ""); def.Month != "2026-10" {
		t.Errorf("default month = %q", def.Month)
	}
}

func TestDominantCurrency(t *testing.T) {
	f := newFixture(t)
	if cur, err := f.svc.dominantCurrency(t.Context()); err != nil || cur != "USD" {
		t.Errorf("no accounts = %q, %v", cur, err)
	}
	f.connect()
	for _, row := range []string{"('x1', 1, 'EUR')", "('x2', 1, 'EUR')"} {
		if _, err := f.store.DB().Exec(`INSERT INTO money_accounts (account_id, item_id, currency, updated_at) VALUES ` + row[:len(row)-1] + `, '2026-10-20T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
	}
	if cur, _ := f.svc.dominantCurrency(t.Context()); cur != "EUR" {
		t.Errorf("most common = %q", cur)
	}
}
