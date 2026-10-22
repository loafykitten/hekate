package money

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

func (s *Service) trendsTool() agent.Tool {
	return agent.Tool{
		Name: "money_trends",
		Description: "How the owner's spending and income move month by month: money out and in for each of the last 6 complete months (up to 24) " +
			"before the current one, the average over exactly those months, the highest and lowest, and the top payees over the same " +
			"months. The current month so far is shown separately. Filter by a category (e.g. Food and drink) or by " +
			"payee words. With no filter it also shows each month's top categories. Use it for 'how much do I spend on food each " +
			"month?', 'is my spending going up?', 'what did I spend at X over the year?' or comparing months. Transfers between their own accounts and loan payments are not counted as spending. Read-only. " + untrustedNote,
		Schema: schema(`{
			"months":{"type":"integer","description":"How many complete months before the current one. Default 6, at most 24."},
			"category":{"type":"string","description":"Only this category, e.g. Food and drink, Transportation, Rent and utilities."},
			"q":{"type":"string","description":"Only transactions with these words in the name or merchant."}}`),
		Effect:  agent.EffectRead,
		Label:   "Comparing your months",
		Timeout: toolTimeout,
		Run: s.guarded(func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Months   int    `json:"months"`
				Category string `json:"category"`
				Q        string `json:"q"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.Months <= 0 {
				a.Months = defaultTrendMonths
			}
			a.Months = min(a.Months, maxTrendMonths)
			content, err := s.describeTrends(ctx, a.Months, strings.TrimSpace(a.Category), strings.TrimSpace(a.Q))
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: content, Stored: keptNote("Compared spending across", plural(a.Months, "month", "months"))}, nil
		}),
	}
}

func (s *Service) describeTrends(ctx context.Context, n int, category, q string) (string, error) {
	currency, err := s.dominantCurrency(ctx)
	if err != nil {
		return "", err
	}
	today := s.today()
	// n complete months before the current one, then the current (partial) month.
	months := make([]string, n+1)
	for i := range months {
		months[i] = time.Date(today.Year(), today.Month()-time.Month(n-i), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	}
	periodStart := months[0] + "-01"
	periodEnd := time.Date(today.Year(), today.Month(), 0, 0, 0, 0, 0, time.UTC).Format(time.DateOnly) // last day of the month before
	var first string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(date), '') FROM money_transactions WHERE currency = ?`, currency).Scan(&first); err != nil {
		return "", fmt.Errorf("read first transaction: %w", err)
	}
	f := rowFilter{from: months[0] + "-01", to: today.Format(time.DateOnly), text: q}
	if category != "" {
		f.category = normalizeCategory(category)
	}
	rows, err := s.loadRows(ctx, currency, f)
	if err != nil {
		return "", err
	}
	trends, payees := buildTrends(rows, months, first, today, category == "" && q == "")

	var sb strings.Builder
	what := "all spending"
	switch {
	case category != "" && q != "":
		what = fmt.Sprintf("%s matching %q", CategoryLabel(f.category), oneLine(q, 60))
	case category != "":
		what = CategoryLabel(f.category)
	case q != "":
		what = fmt.Sprintf("payees matching %q", oneLine(q, 60))
	}
	fmt.Fprintf(&sb, "Month by month, %s, the last %s: %s to %s (%s), then the current month so far. Out is spending (transfers between own accounts and loan payments aren't counted); pending counts.\n",
		what, plural(n, "complete month", "complete months"), periodStart, periodEnd, currency)
	var done []monthTrend
	for _, m := range trends {
		if !m.partial && !m.noData && !m.startsLate {
			done = append(done, m)
		}
	}
	if len(done) < n {
		fmt.Fprintf(&sb, "Only %d of those %d months have complete data, so the average is over %d, not %d.\n", len(done), n, len(done), n)
	}
	if len(done) > 0 {
		var out, in int64
		hi, lo := done[0], done[0]
		for _, m := range done {
			out, in = out+m.spent, in+m.received
			if m.spent > hi.spent {
				hi = m
			}
			if m.spent < lo.spent {
				lo = m
			}
		}
		k := int64(len(done))
		fmt.Fprintf(&sb, "Average over the %s (total out %s, total in %s, divided by %d): out %s, in %s. Highest out: %s (%s). Lowest out: %s (%s).\n",
			plural(len(done), "complete month", "complete months"), centsText(out, currency), centsText(in, currency), k,
			centsText(out/k, currency), centsText(in/k, currency),
			hi.month, centsText(hi.spent, currency), lo.month, centsText(lo.spent, currency))
	} else {
		sb.WriteString("No complete month in this range has data yet, so there is no average.\n")
	}
	for _, m := range trends[:len(trends)-1] {
		sb.WriteString(trendLine(m, currency))
		sb.WriteByte('\n')
	}
	if cur := trends[len(trends)-1]; !cur.noData {
		sb.WriteString("Current month, separate from the average and the top payees (only through today, so it looks low next to full months):\n")
		sb.WriteString(trendLine(cur, currency))
		sb.WriteByte('\n')
	}
	if len(payees) == 0 {
		sb.WriteString("No spending found for this filter.")
		return strings.TrimRight(sb.String(), "\n"), nil
	}
	fmt.Fprintf(&sb, "Top payees by money out over the same complete months, %s to %s (payee | total | count):\n", periodStart, periodEnd)
	lines := make([]string, len(payees))
	for i, p := range payees {
		lines[i] = fmt.Sprintf("%s | %s | %s", oneLine(p.payee, 80), centsText(p.cents, currency), plural(p.count, "transaction", "transactions"))
	}
	sb.WriteString(fenced(lines))
	return sb.String(), nil
}

// trendLine describes one month. It holds only numbers and fixed category labels.
func trendLine(m monthTrend, currency string) string {
	if m.noData {
		return m.month + ": no data (the history starts later)"
	}
	var sb strings.Builder
	sb.WriteString(m.month)
	if m.startsLate {
		sb.WriteString(" (history starts mid-month: partial)")
	}
	if m.partial {
		sb.WriteString(" (so far, partial)")
	}
	fmt.Fprintf(&sb, ": out %s | in %s", centsText(m.spent, currency), centsText(m.received, currency))
	if m.transfers > 0 {
		fmt.Fprintf(&sb, " | transfers and loan payments %s", centsText(m.transfers, currency))
	}
	if len(m.categories) > 0 {
		parts := make([]string, len(m.categories))
		for i, c := range m.categories {
			parts[i] = fmt.Sprintf("%s %.2f", c.label, dollars(c.spent))
		}
		sb.WriteString(" | top: " + strings.Join(parts, ", "))
	}
	return sb.String()
}
