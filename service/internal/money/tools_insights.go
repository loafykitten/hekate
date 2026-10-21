package money

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/web"
)

const (
	defaultUpcomingDays = 30
	maxUpcomingDays     = 60
	defaultTrendMonths  = 6
	maxTrendMonths      = 24
	maxRecurringLines   = 40
)

const estimateNote = "These are estimates from past patterns, not scheduled payments: real dates and amounts can differ."

// centsText formats an amount in cents, e.g. 1599 as "15.99 USD".
func centsText(c int64, currency string) string { return money(dollars(c), currency) }

// fenced wraps lines of bank text in nonce-delimited untrusted markers.
func fenced(lines []string) string {
	id := web.Nonce()
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n<untrusted_bank boundary=%q>\n", untrustedNote, id)
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "</untrusted_bank boundary=%q>", id)
	return sb.String()
}

func (s *Service) recurringTool() agent.Tool {
	return agent.Tool{
		Name: "money_recurring",
		Description: "Find the owner's recurring payments (subscriptions, rent, bills, loan payments) and recurring income (paydays) " +
			"from the last 13 months of transactions, with each one's rhythm, typical amount, last charge, when the next one is " +
			"expected, and whether the amount recently changed. Also the monthly total of bills and of income. Use it for 'what " +
			"subscriptions do I have?', 'did any of my bills go up?', 'what am I paying every month?' or 'when do I get paid?'. " +
			"Detected from past patterns, so a brand-new subscription (fewer than 3 charges) won't show yet. Transfers between " +
			"their own accounts are left out. Read-only. " + untrustedNote,
		Schema:  schema(`{"kind":{"type":"string","enum":["bills","income","all"],"description":"bills: money going out. income: paydays and other regular money in. Default all."}}`),
		Effect:  agent.EffectRead,
		Label:   "Looking for recurring payments",
		Timeout: toolTimeout,
		Run: s.guarded(func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Kind string `json:"kind"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			kind := strings.ToLower(strings.TrimSpace(a.Kind))
			if kind == "" {
				kind = "all"
			}
			if kind != "all" && kind != "bills" && kind != "income" {
				return agent.Result{}, fmt.Errorf("kind must be bills, income or all")
			}
			rep, err := s.recurringReport(ctx)
			if err != nil {
				return agent.Result{}, err
			}
			content, txns, highlight := describeRecurring(rep, kind)
			res := agent.Result{Content: content, Stored: keptNote("Looked for recurring payments in", "the past year of transactions")}
			if len(txns) > 0 {
				res.Card = TransactionsCard("Recurring payments", txns, highlight)
			}
			return res, nil
		}),
	}
}

// describeRecurring lays the detected items out, and returns the latest charge of
// each as transactions for the card (changed amounts highlighted).
func describeRecurring(rep recurringReport, kind string) (string, []Transaction, []string) {
	var bills, income []recurring
	var billMonthly, incomeMonthly float64
	for _, it := range rep.items {
		if it.income {
			income = append(income, it)
			incomeMonthly += it.monthly()
		} else {
			bills = append(bills, it)
			billMonthly += it.monthly()
		}
	}
	var head strings.Builder
	fmt.Fprintf(&head, "Recurring payments found in the last %d months (detected from past patterns; amounts in %s). Dates are YYYY-MM-DD.\n", recurringMonths, rep.currency)
	if kind != "income" {
		fmt.Fprintf(&head, "Recurring bills: about %s a month (%s).\n", centsText(int64(billMonthly+0.5), rep.currency), plural(len(bills), "item", "items"))
	}
	if kind != "bills" {
		fmt.Fprintf(&head, "Recurring income: about %s a month (%s).\n", centsText(int64(incomeMonthly+0.5), rep.currency), plural(len(income), "item", "items"))
	}
	head.WriteString("The monthly totals use each item's current amount, scaled to a month.\n")
	if rep.stopped > 0 {
		fmt.Fprintf(&head, "Not listed: %s that looked regular once but have not recurred lately (probably cancelled).\n", plural(rep.stopped, "other payee", "other payees"))
	}
	var lines []string
	var txns []Transaction
	var highlight []string
	section := func(title string, list []recurring) {
		if len(list) == 0 {
			return
		}
		lines = append(lines, title)
		for i, it := range list {
			if i == maxRecurringLines {
				lines = append(lines, fmt.Sprintf("(%d more not shown)", len(list)-i))
				break
			}
			lines = append(lines, recurringLine(it, rep.currency, rep.today))
			txns = append(txns, it.latest.transaction())
			if it.changed {
				highlight = append(highlight, it.latest.id)
			}
		}
	}
	if kind != "income" {
		section("Bills:", bills)
	}
	if kind != "bills" {
		section("Income:", income)
	}
	if len(lines) == 0 {
		head.WriteString("Nothing recurring found: a payee needs at least 3 charges in a regular rhythm (2 for yearly).")
		return head.String(), nil, nil
	}
	return head.String() + fenced(lines), txns, highlight
}

// recurringLine describes one item. The payee is the bank's text, so the line sits in a wrapper.
func recurringLine(it recurring, currency string, today time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s | %s | typical %s | last %s on %s", oneLine(it.payee, 80), it.cadence.name, centsText(it.typical, currency),
		centsText(it.last, currency), it.lastDay.Format(time.DateOnly))
	if it.next.Before(today) {
		fmt.Fprintf(&sb, " | was expected %s and hasn't shown up yet", it.next.Format(time.DateOnly))
	} else {
		fmt.Fprintf(&sb, " | next expected about %s", it.next.Format(time.DateOnly))
	}
	if it.changed {
		fmt.Fprintf(&sb, " | CHANGED: from %s to %s", centsText(it.previous, currency), centsText(it.last, currency))
	}
	return sb.String()
}

func (s *Service) upcomingTool() agent.Tool {
	return agent.Tool{
		Name: "money_upcoming",
		Description: "Look ahead at the owner's cash flow: the recurring bills and paydays expected over the next 30 days (up to 60), " +
			"with the checking balance after each, the lowest point, and whether the money in checking covers what is due before " +
			"the next payday. Use it for 'will I make it to payday?', 'can I afford this?', 'what bills are coming up?' or 'how " +
			"much should I keep in checking?'. These are estimates from past patterns (see money_recurring), not scheduled " +
			"payments, and everyday spending (food, gas) isn't in them. Read-only. " + untrustedNote,
		Schema:  schema(`{"days":{"type":"integer","description":"How many days ahead. Default 30, at most 60."}}`),
		Effect:  agent.EffectRead,
		Label:   "Looking ahead at your money",
		Timeout: toolTimeout,
		Run: s.guarded(func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Days int `json:"days"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			days := a.Days
			if days <= 0 {
				days = defaultUpcomingDays
			}
			days = min(days, maxUpcomingDays)
			rep, err := s.recurringReport(ctx)
			if err != nil {
				return agent.Result{}, err
			}
			accounts, err := s.Accounts(ctx)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{
				Content: describeUpcoming(rep, cashOnHand(accounts, rep.currency), days),
				Stored:  keptNote("Looked ahead at", "upcoming bills and income"),
			}, nil
		}),
	}
}

// describeUpcoming projects the recurring items over the next days from the cash on hand.
func describeUpcoming(rep recurringReport, c cash, days int) string {
	cur := rep.currency
	end := rep.today.AddDate(0, 0, days)
	// Project past the window too, to find the next payday.
	all := project(rep.items, rep.today, end.AddDate(0, 0, 62))
	var window []occurrence
	var paydayAt *occurrence
	for i, o := range all {
		if !o.day.After(end) {
			window = append(window, o)
		}
		if o.income && paydayAt == nil {
			paydayAt = &all[i]
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Cash look-ahead for the next %d days (through %s). %s\n", days, end.Format(time.DateOnly), estimateNote)
	sb.WriteString("Everyday spending (food, gas, shopping) is not included, so the real balance will run lower.\n")
	if c.checkingN > 0 {
		fmt.Fprintf(&sb, "Checking now: %s (available balances).", centsText(c.checking, cur))
	} else {
		sb.WriteString("No checking account balance is available, so running balances are skipped.")
	}
	if c.savings != 0 {
		fmt.Fprintf(&sb, " Savings and other deposit accounts: %s.", centsText(c.savings, cur))
	}
	if c.cards > 0 {
		fmt.Fprintf(&sb, " Credit cards owe %s in total (%s).", centsText(c.owed, cur), plural(c.cards, "card", "cards"))
	}
	sb.WriteString("\n")
	if len(rep.items) == 0 {
		sb.WriteString("No recurring bills or income were detected, so there is nothing to project.")
		return sb.String()
	}
	var out, in int64
	for _, o := range window {
		if o.income {
			in += o.cents
		} else {
			out -= o.cents
		}
	}
	fmt.Fprintf(&sb, "Expected in the window: bills %s, income %s.\n", centsText(out, cur), centsText(in, cur))
	if c.checkingN > 0 {
		bal, low, lowDay := c.checking, c.checking, ""
		var lines []string
		firstNeg := ""
		for _, o := range window {
			bal += o.cents
			if bal < low {
				low, lowDay = bal, o.day.Format(time.DateOnly)
			}
			if bal < 0 && firstNeg == "" {
				firstNeg = o.day.Format(time.DateOnly)
			}
			lines = append(lines, upcomingLine(o, bal, true, cur))
		}
		if lowDay == "" {
			fmt.Fprintf(&sb, "Lowest point: right now, at %s.\n", centsText(low, cur))
		} else {
			fmt.Fprintf(&sb, "Lowest point: %s on %s.\n", centsText(low, cur), lowDay)
		}
		if firstNeg != "" {
			fmt.Fprintf(&sb, "WARNING: on these estimates checking dips below zero on %s (lowest %s) unless money is moved in.\n", firstNeg, centsText(low, cur))
		} else {
			sb.WriteString("On these estimates checking stays above zero through the window.\n")
		}
		sb.WriteString(paydayText(all, paydayAt, c, rep.today, cur))
		if len(lines) > 0 {
			sb.WriteString("Date | amount | what | checking after\n")
			sb.WriteString(fenced(lines))
		}
		return strings.TrimRight(sb.String(), "\n")
	}
	lines := make([]string, 0, len(window))
	for _, o := range window {
		lines = append(lines, upcomingLine(o, 0, false, cur))
	}
	if len(lines) > 0 {
		sb.WriteString(fenced(lines))
	}
	return sb.String()
}

// paydayText compares what is due before the next payday with the checking balance.
func paydayText(all []occurrence, payday *occurrence, c cash, today time.Time, currency string) string {
	if payday == nil {
		return "No recurring income was detected, so there is no payday to compare against.\n"
	}
	var due int64
	for _, o := range all {
		if !o.income && o.day.Before(payday.day) {
			due -= o.cents
		}
	}
	when := payday.day.Format(time.DateOnly)
	switch {
	case payday.late:
		when = "due now (expected earlier, not seen yet)"
	case payday.day.Equal(today):
		when = "today"
	}
	left := c.checking - due
	verdict := fmt.Sprintf("leaving %s", centsText(left, currency))
	if left < 0 {
		verdict = fmt.Sprintf("short by %s", centsText(-left, currency))
	}
	return fmt.Sprintf("Next payday: %s. Bills expected before then: %s, against %s in checking now: %s.\n",
		when, centsText(due, currency), centsText(c.checking, currency), verdict)
}

func upcomingLine(o occurrence, balance int64, showBalance bool, currency string) string {
	kind := "bill"
	if o.income {
		kind = "income"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s | %s | %s: %s", o.day.Format(time.DateOnly), centsText(o.cents, currency), kind, oneLine(o.payee, 80))
	if o.late {
		sb.WriteString(" (expected earlier, not seen yet)")
	}
	if showBalance {
		fmt.Fprintf(&sb, " | checking after: %s", centsText(balance, currency))
	}
	return sb.String()
}
