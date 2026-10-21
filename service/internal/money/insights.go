package money

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
)

// History the recurring detector reads, and how many top payees trends lists.
const (
	recurringMonths = 13
	maxTopPayees    = 10
)

// txRow is one stored transaction, as the insight tools read it. Name and
// merchant are the bank's text.
type txRow struct {
	id, accountID, account string
	date                   string
	day                    time.Time // date at midnight UTC, for day arithmetic
	name, merchant         string
	cents                  int64
	currency               string
	pending                bool
	category               string
}

// payee is who the money went to or came from: the merchant, else the name.
func (r txRow) payee() string {
	if m := strings.TrimSpace(r.merchant); m != "" {
		return m
	}
	return strings.TrimSpace(r.name)
}

func (r txRow) transaction() Transaction {
	t := Transaction{
		ID: r.id, AccountID: r.accountID, Account: r.account, Date: r.date, Name: r.name,
		Merchant: nonEmpty(r.merchant), Amount: dollars(r.cents), Currency: r.currency, Pending: r.pending,
	}
	if r.category != "" {
		c, l := r.category, CategoryLabel(r.category)
		t.Category, t.CategoryLabel = &c, &l
	}
	return t
}

func (r txRow) isTransferOrLoan() bool {
	return r.category == catTransferIn || r.category == catTransferOut || r.category == catLoanPayments
}

// payeeKey normalizes a payee so the same biller groups together across months:
// lowercase letters only, so digits, '#', '*', store numbers and punctuation drop
// out and runs of spaces collapse.
func payeeKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// today is the owner's current date at midnight UTC.
func (s *Service) today() time.Time {
	n := s.now().In(s.loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// rowFilter narrows loadRows. Empty fields don't filter.
type rowFilter struct {
	from, to string // inclusive dates
	category string // Plaid primary, normalized
	text     string // words in the name or merchant
}

// loadRows reads transactions in one currency, oldest first.
func (s *Service) loadRows(ctx context.Context, currency string, f rowFilter) ([]txRow, error) {
	where := []string{`t.currency = ?`}
	args := []any{currency}
	if f.from != "" {
		where = append(where, `t.date >= ?`)
		args = append(args, f.from)
	}
	if f.to != "" {
		where = append(where, `t.date <= ?`)
		args = append(args, f.to)
	}
	if f.category != "" {
		where = append(where, `t.category = ?`)
		args = append(args, f.category)
	}
	if f.text != "" {
		pat := "%" + likeEscape(f.text) + "%"
		where = append(where, `(t.name LIKE ? ESCAPE '\' OR t.merchant LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	query := `
		SELECT t.transaction_id, t.account_id, COALESCE(a.name, ''), t.date, t.name, t.merchant, t.amount_cents, t.currency, t.pending, t.category
		FROM money_transactions t LEFT JOIN money_accounts a ON a.account_id = t.account_id`
	query += ` WHERE ` + strings.Join(where, ` AND `) + ` ORDER BY t.date, t.transaction_id` //nolint:gosec // the clauses are fixed text; every value is bound
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []txRow
	for rows.Next() {
		var r txRow
		if err := rows.Scan(&r.id, &r.accountID, &r.account, &r.date, &r.name, &r.merchant, &r.cents, &r.currency, &r.pending, &r.category); err != nil {
			return nil, fmt.Errorf("read a transaction: %w", err)
		}
		day, err := time.Parse(time.DateOnly, r.date)
		if err != nil {
			continue
		}
		r.day = day
		out = append(out, r)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// cadence is a recurring rhythm: the median gaps in days it covers, how many
// times a month it happens, and, for monthly and longer ones, how many calendar
// months apart (0: it steps by its median gap in days).
type cadence struct {
	name     string
	lo, hi   float64
	perMonth float64
	minSeen  int
	months   int
}

var cadences = []cadence{
	{"weekly", 6, 8, 52.0 / 12, 3, 0},
	{"every two weeks", 13, 16, 26.0 / 12, 3, 0},
	{"monthly", 27, 33, 1, 3, 1},
	{"quarterly", 85, 95, 1.0 / 3, 3, 3},
	{"yearly", 355, 375, 1.0 / 12, 2, 12},
}

// recurring is a payee with a regular rhythm, money out (a bill) or in (income).
type recurring struct {
	payee    string // the bank's text
	income   bool
	cadence  cadence
	gap      int   // median days between occurrences
	typical  int64 // median amount, cents (positive)
	last     int64 // latest amount, cents (positive)
	previous int64 // the one before it
	changed  bool
	expected int64 // what the next one should be: the new amount if it changed, else the typical
	seen     int
	lastDay  time.Time
	next     time.Time
	latest   txRow
}

// monthly is the item's cost or pay per month, in cents.
func (r recurring) monthly() float64 { return float64(r.expected) * r.cadence.perMonth }

func medianInt64(v []int64) int64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2] + 1) / 2
}

func medianFloat(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// dropPendingDuplicates removes a pending row when a posted one with the same
// payee and amount sits within a week of it: the same charge seen twice.
func dropPendingDuplicates(rows []txRow) []txRow {
	var out []txRow
	for _, r := range rows {
		dup := false
		if r.pending {
			k := payeeKey(r.payee())
			for _, o := range rows {
				if !o.pending && o.cents == r.cents && payeeKey(o.payee()) == k && math.Abs(o.day.Sub(r.day).Hours()) <= 7*24 {
					dup = true
					break
				}
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// detectRecurring finds payees with a regular cadence. Transfers between the
// owner's own accounts are left out; loan payments and rent stay in as bills.
// Rows are oldest first. A pattern whose last occurrence is long overdue has
// stopped: it is counted in stopped, not returned.
func detectRecurring(rows []txRow, today time.Time) (items []recurring, stopped int) {
	type groupKey struct {
		key    string
		income bool
	}
	groups := map[groupKey][]txRow{}
	for _, r := range dropPendingDuplicates(rows) {
		if r.category == catTransferIn || r.category == catTransferOut || r.cents == 0 {
			continue
		}
		k := payeeKey(r.payee())
		if k == "" {
			continue
		}
		gk := groupKey{k, r.cents > 0}
		groups[gk] = append(groups[gk], r)
	}
	for gk, g := range groups {
		// One entry per day: two deposits on the same day are one payday.
		var days []txRow
		for _, r := range g {
			if n := len(days); n > 0 && days[n-1].date == r.date {
				days[n-1].cents += r.cents
				days[n-1].id, days[n-1].pending = r.id, r.pending
				continue
			}
			days = append(days, r)
		}
		if it, ok := classify(days, gk.income); ok {
			if today.Sub(it.lastDay).Hours()/24 > 1.5*float64(it.gap)+5 {
				stopped++
				continue
			}
			items = append(items, it)
		}
	}
	slices.SortFunc(items, func(a, b recurring) int {
		if a.monthly() != b.monthly() {
			if a.monthly() > b.monthly() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.payee, b.payee)
	})
	return items, stopped
}

// classify decides whether one payee's days (oldest first) keep a regular rhythm.
func classify(days []txRow, income bool) (recurring, bool) {
	if len(days) < 2 {
		return recurring{}, false
	}
	gaps := make([]float64, 0, len(days)-1)
	for i := 1; i < len(days); i++ {
		gaps = append(gaps, days[i].day.Sub(days[i-1].day).Hours()/24)
	}
	m := medianFloat(gaps)
	idx := slices.IndexFunc(cadences, func(c cadence) bool { return m >= c.lo && m <= c.hi })
	if idx < 0 || len(days) < cadences[idx].minSeen {
		return recurring{}, false
	}
	near := 0
	for _, g := range gaps {
		if math.Abs(g-m) <= 0.25*m {
			near++
		}
	}
	if near*2 <= len(gaps) {
		return recurring{}, false
	}
	amounts := make([]int64, len(days))
	for i, d := range days {
		amounts[i] = abs64(d.cents)
	}
	latest := days[len(days)-1]
	it := recurring{
		income: income, cadence: cadences[idx], gap: int(math.Round(m)), typical: medianInt64(amounts),
		last: amounts[len(amounts)-1], previous: amounts[len(amounts)-2], seen: len(days), lastDay: latest.day, latest: latest,
		payee: latest.payee(),
	}
	diff := abs64(it.last - it.previous)
	it.changed = diff > 100 && diff*100 > 5*it.previous
	it.expected = it.typical
	if it.changed {
		it.expected = it.last
	}
	it.next = it.step(it.lastDay)
	return it, true
}

// step is the occurrence after d. Monthly and longer rhythms keep the day of the
// month (rent on the 1st stays on the 1st; the 31st becomes the last day of a
// shorter month); weekly ones step by their median gap.
func (it recurring) step(d time.Time) time.Time {
	if it.cadence.months == 0 {
		return d.AddDate(0, 0, it.gap)
	}
	y, m, _ := d.Date()
	first := time.Date(y, m+time.Month(it.cadence.months), 1, 0, 0, 0, 0, d.Location())
	lastDay := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(it.lastDay.Day(), lastDay), 0, 0, 0, 0, d.Location())
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// recurringReport is what money_recurring and money_upcoming both read.
type recurringReport struct {
	currency string
	items    []recurring
	stopped  int
	today    time.Time
}

func (s *Service) recurringReport(ctx context.Context) (recurringReport, error) {
	currency, err := s.dominantCurrency(ctx)
	if err != nil {
		return recurringReport{}, err
	}
	today := s.today()
	rows, err := s.loadRows(ctx, currency, rowFilter{
		from: today.AddDate(0, -recurringMonths, 0).Format(time.DateOnly), to: today.AddDate(0, 0, 7).Format(time.DateOnly),
	})
	if err != nil {
		return recurringReport{}, err
	}
	items, stopped := detectRecurring(rows, today)
	return recurringReport{currency: currency, items: items, stopped: stopped, today: today}, nil
}

// occurrence is one expected payment or payday in the look-ahead.
type occurrence struct {
	day    time.Time
	cents  int64 // signed: money in (+) or out (-)
	payee  string
	income bool
	late   bool // its date has passed without it showing up
}

// project lists each item's expected occurrences from today to horizon, soonest
// first, bills before income on the same day (the cautious order).
func project(items []recurring, today, horizon time.Time) []occurrence {
	var out []occurrence
	for _, it := range items {
		sign := int64(-1)
		if it.income {
			sign = 1
		}
		for d := it.next; !d.After(horizon); d = it.step(d) {
			o := occurrence{day: d, cents: sign * it.expected, payee: it.payee, income: it.income}
			if d.Before(today) {
				o.day, o.late = today, true
			}
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(a, b occurrence) int {
		if c := a.day.Compare(b.day); c != 0 {
			return c
		}
		if a.income != b.income {
			if a.income {
				return 1
			}
			return -1
		}
		return strings.Compare(a.payee, b.payee)
	})
	return out
}

// cash is what the owner has on hand, in cents.
type cash struct {
	checking, savings, owed int64
	checkingN, cards        int
}

// cashOnHand sums the available (else current) balances of checking accounts,
// other deposit accounts apart, and what the credit cards owe.
func cashOnHand(accounts []Account, currency string) cash {
	var c cash
	for _, a := range accounts {
		if a.Currency != currency {
			continue
		}
		switch a.Type {
		case "depository":
			bal := a.Balance.Available
			if bal == nil {
				bal = a.Balance.Current
			}
			if bal == nil {
				continue
			}
			cents := int64(math.Round(*bal * 100))
			if strings.EqualFold(a.Subtype, "checking") {
				c.checking += cents
				c.checkingN++
			} else {
				c.savings += cents
			}
		case "credit":
			if a.Balance.Current != nil {
				c.owed += int64(math.Round(*a.Balance.Current * 100))
				c.cards++
			}
		}
	}
	return c
}

// monthTrend is one calendar month of the trends tool.
type monthTrend struct {
	month      string
	spent      int64
	received   int64
	transfers  int64
	categories []trendCategory
	partial    bool
	noData     bool // before the first stored transaction
}

type trendCategory struct {
	label string
	spent int64
}

type payeeTotal struct {
	payee string
	cents int64
	count int
}

// buildTrends groups rows by month for the given months and totals the top
// payees by money out. firstData is the date of the first stored transaction.
func buildTrends(rows []txRow, months []string, firstData string, today time.Time, withCategories bool) ([]monthTrend, []payeeTotal) {
	byMonth := map[string][]flow{}
	totals := map[string]*payeeTotal{}
	for _, r := range rows {
		m := r.date[:7]
		byMonth[m] = append(byMonth[m], flow{r.category, r.cents})
		if r.cents < 0 && !r.isTransferOrLoan() {
			k := payeeKey(r.payee())
			if k == "" {
				k = "category " + r.category
			}
			p := totals[k]
			if p == nil {
				p = &payeeTotal{}
				totals[k] = p
			}
			p.cents -= r.cents
			p.count++
			p.payee = r.payee() // rows are oldest first: the latest spelling wins
		}
	}
	cur := today.Format("2006-01")
	var out []monthTrend
	for _, m := range months {
		t := tallyFlows(byMonth[m])
		mt := monthTrend{month: m, spent: t.spent, received: t.received, transfers: t.transfers, partial: m == cur}
		if len(firstData) < 7 || m < firstData[:7] {
			mt.noData = true
		}
		if withCategories {
			for cat, c := range t.byCategory {
				mt.categories = append(mt.categories, trendCategory{CategoryLabel(cat), c.spent})
			}
			slices.SortFunc(mt.categories, func(a, b trendCategory) int {
				if a.spent != b.spent {
					if a.spent > b.spent {
						return -1
					}
					return 1
				}
				return strings.Compare(a.label, b.label)
			})
			if len(mt.categories) > 3 {
				mt.categories = mt.categories[:3]
			}
		}
		out = append(out, mt)
	}
	payees := make([]payeeTotal, 0, len(totals))
	for _, p := range totals {
		payees = append(payees, *p)
	}
	slices.SortFunc(payees, func(a, b payeeTotal) int {
		if a.cents != b.cents {
			if a.cents > b.cents {
				return -1
			}
			return 1
		}
		return strings.Compare(a.payee, b.payee)
	})
	if len(payees) > maxTopPayees {
		payees = payees[:maxTopPayees]
	}
	return out, payees
}
