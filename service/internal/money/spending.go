package money

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Plaid's primary categories that are not spending: moving money between the
// owner's own accounts, and paying down a loan or card.
const (
	catTransferIn   = "TRANSFER_IN"
	catTransferOut  = "TRANSFER_OUT"
	catLoanPayments = "LOAN_PAYMENTS"
	catOther        = "OTHER"
)

var categoryLabels = map[string]string{
	"INCOME":                    "Income",
	catTransferIn:               "Transfers in",
	catTransferOut:              "Transfers out",
	catLoanPayments:             "Loan payments",
	"BANK_FEES":                 "Bank fees",
	"ENTERTAINMENT":             "Entertainment",
	"FOOD_AND_DRINK":            "Food and drink",
	"GENERAL_MERCHANDISE":       "General merchandise",
	"HOME_IMPROVEMENT":          "Home improvement",
	"MEDICAL":                   "Medical",
	"PERSONAL_CARE":             "Personal care",
	"GENERAL_SERVICES":          "General services",
	"GOVERNMENT_AND_NON_PROFIT": "Government and non-profit",
	"TRANSPORTATION":            "Transportation",
	"TRAVEL":                    "Travel",
	"RENT_AND_UTILITIES":        "Rent and utilities",
	catOther:                    "Other",
}

// CategoryLabel is a Plaid primary category in words: FOOD_AND_DRINK is "Food and
// drink". A category Plaid adds later is humanized the same way.
func CategoryLabel(c string) string {
	if l, ok := categoryLabels[c]; ok {
		return l
	}
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(c, "_", " ")))
	if len(words) == 0 {
		return categoryLabels[catOther]
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

// SpendingCategory is one category's money out for a month and the month before.
type SpendingCategory struct {
	Category string  `json:"category"`
	Label    string  `json:"label"`
	Spent    float64 `json:"spent"`
	Count    int     `json:"count"`
	Previous float64 `json:"previous"`
}

// Spending is one calendar month of money in and out. Transfers between the
// owner's own accounts and loan payments are left out of spent and listed apart.
type Spending struct {
	Month         string             `json:"month"`
	Currency      string             `json:"currency"`
	Spent         float64            `json:"spent"`
	Received      float64            `json:"received"`
	Categories    []SpendingCategory `json:"categories"`
	PreviousSpent float64            `json:"previous_spent"`
	Transfers     float64            `json:"transfers"`
}

// flow is one transaction as spending sees it: cents in (+) or out (-).
type flow struct {
	category string
	cents    int64
}

type tally struct {
	spent, received, transfers int64
	byCategory                 map[string]*catTally
}

type catTally struct {
	spent int64
	count int
}

// tallyFlows adds up a month. Money out is spent, except transfers out and loan
// payments, which are transfers. Money in is received, except transfers in.
// Pending transactions count.
func tallyFlows(flows []flow) tally {
	t := tally{byCategory: map[string]*catTally{}}
	for _, f := range flows {
		cat := f.category
		if cat == "" {
			cat = catOther
		}
		switch {
		case f.cents < 0 && (cat == catTransferOut || cat == catLoanPayments):
			t.transfers -= f.cents
		case f.cents < 0:
			t.spent -= f.cents
			c := t.byCategory[cat]
			if c == nil {
				c = &catTally{}
				t.byCategory[cat] = c
			}
			c.spent -= f.cents
			c.count++
		case cat != catTransferIn:
			t.received += f.cents
		}
	}
	return t
}

// summarize builds the month's figures from its flows and the month before's.
func summarize(month, currency string, this, before []flow) Spending {
	cur, prev := tallyFlows(this), tallyFlows(before)
	out := Spending{
		Month: month, Currency: currency, Spent: dollars(cur.spent), Received: dollars(cur.received),
		PreviousSpent: dollars(prev.spent), Transfers: dollars(cur.transfers), Categories: []SpendingCategory{},
	}
	for cat, c := range cur.byCategory {
		sc := SpendingCategory{Category: cat, Label: CategoryLabel(cat), Spent: dollars(c.spent), Count: c.count}
		if p := prev.byCategory[cat]; p != nil {
			sc.Previous = dollars(p.spent)
		}
		out.Categories = append(out.Categories, sc)
	}
	slices.SortFunc(out.Categories, func(a, b SpendingCategory) int {
		if a.Spent != b.Spent {
			if a.Spent > b.Spent {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Label, b.Label)
	})
	return out
}

// Spending totals a calendar month (YYYY-MM, in the owner's zone; "" is this
// month) with the month before for comparison. Only transactions in the
// accounts' currency count: with mixed currencies that is the most common one
// and the rest are left out, since cents of different currencies don't add up.
func (s *Service) Spending(ctx context.Context, month string) (Spending, error) {
	if month == "" {
		month = s.now().In(s.loc).Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, s.loc)
	if err != nil {
		return Spending{}, httpx.BadRequest("month must look like 2026-10")
	}
	prevStart, nextStart := start.AddDate(0, -1, 0), start.AddDate(0, 1, 0)
	currency, err := s.dominantCurrency(ctx)
	if err != nil {
		return Spending{}, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT date, category, amount_cents FROM money_transactions WHERE date >= ? AND date < ? AND currency = ?`,
		prevStart.Format(time.DateOnly), nextStart.Format(time.DateOnly), currency)
	if err != nil {
		return Spending{}, fmt.Errorf("read spending: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var this, before []flow
	startDay := start.Format(time.DateOnly)
	for rows.Next() {
		var date string
		var f flow
		if err := rows.Scan(&date, &f.category, &f.cents); err != nil {
			return Spending{}, fmt.Errorf("read spending: %w", err)
		}
		if date >= startDay {
			this = append(this, f)
		} else {
			before = append(before, f)
		}
	}
	if err := rows.Err(); err != nil {
		return Spending{}, fmt.Errorf("read spending: %w", err)
	}
	return summarize(month, currency, this, before), nil
}

// dominantCurrency is the currency most accounts are in; USD with no accounts.
func (s *Service) dominantCurrency(ctx context.Context) (string, error) {
	var cur string
	err := s.db.QueryRowContext(ctx,
		`SELECT currency FROM money_accounts GROUP BY currency ORDER BY COUNT(*) DESC, currency LIMIT 1`).Scan(&cur)
	if err != nil {
		if isNoRows(err) {
			return "USD", nil
		}
		return "", fmt.Errorf("read currency: %w", err)
	}
	return cur, nil
}
