package money

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// askNote is the rule for anything the owner hasn't labeled.
const askNote = "Ask the owner what these are (a few at a time, the biggest first) and record each answer with money_label_payee; " +
	"don't call one a bill, or guess what it is from its name, until they have said."

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

// grouped splits detected items by what the owner said they are.
type grouped struct {
	bills, subs, income []recurring // confirmed
	unOut, unIn         []recurring // not confirmed yet: money out, money in
}

func groupItems(items []recurring) grouped {
	var g grouped
	for _, it := range items {
		switch it.label.kind {
		case kindBill:
			g.bills = append(g.bills, it)
		case kindSubscription:
			g.subs = append(g.subs, it)
		case kindIncome:
			g.income = append(g.income, it)
		default:
			if it.income {
				g.unIn = append(g.unIn, it)
			} else {
				g.unOut = append(g.unOut, it)
			}
		}
	}
	return g
}

func (g grouped) confirmed() []recurring {
	var out []recurring
	out = append(out, g.bills...)
	out = append(out, g.subs...)
	return append(out, g.income...)
}

// monthlyCents is what the items come to in a month.
func monthlyCents(list []recurring) int64 {
	var t float64
	for _, it := range list {
		t += it.monthly()
	}
	return int64(t + 0.5)
}

func (s *Service) recurringTool() agent.Tool {
	return agent.Tool{
		Name: "money_recurring",
		Description: "Find payments in the owner's last two years of transactions that LOOK recurring (regular rhythm), money out and " +
			"money in, each with a payee id, its rhythm, typical amount, last charge, when the next one is expected and whether the " +
			"amount recently changed. Only what the owner has confirmed is called a bill, subscription or income; the rest are listed " +
			"as 'not confirmed yet', and what they are is the owner's to say (they may not pay rent, for example). Before calling " +
			"anything a bill or planning around it, ask the owner about the unconfirmed ones (a few at a time, the biggest first) and " +
			"record their answers with money_label_payee; don't re-ask the ones already labeled. Use it for 'what subscriptions " +
			"do I have?', 'did anything I pay every month go up?' or 'when do I get paid?'. A payee with fewer than 3 payments won't " +
			"show yet. Transfers between their own accounts are left out. Read-only. " + untrustedNote,
		Schema:  schema(`{"kind":{"type":"string","enum":["bills","income","all"],"description":"bills: regular payments going out. income: regular money coming in. Default all."}}`),
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
	g := groupItems(rep.items)
	cur := rep.currency
	var head strings.Builder
	fmt.Fprintf(&head, "Payments that look recurring in the last %d months (detected from past patterns; amounts in %s; dates YYYY-MM-DD). "+
		"What each one is belongs to the owner: only the ones they confirmed are labeled bill, subscription or income.\n", recurringMonths, cur)
	fmt.Fprintf(&head, "Confirmed by the owner, per month: bills about %s (%s), subscriptions about %s (%s), income about %s (%s).\n",
		centsText(monthlyCents(g.bills), cur), plural(len(g.bills), "item", "items"),
		centsText(monthlyCents(g.subs), cur), plural(len(g.subs), "item", "items"),
		centsText(monthlyCents(g.income), cur), plural(len(g.income), "item", "items"))
	if len(g.unOut)+len(g.unIn) > 0 {
		fmt.Fprintf(&head, "Not confirmed yet: %s going out (about %s a month) and %s coming in (about %s a month). %s\n",
			plural(len(g.unOut), "payment", "payments"), centsText(monthlyCents(g.unOut), cur),
			plural(len(g.unIn), "regular deposit", "regular deposits"), centsText(monthlyCents(g.unIn), cur), askNote)
	}
	head.WriteString("Monthly figures use each item's current amount, scaled to a month.\n")
	if rep.hidden > 0 {
		fmt.Fprintf(&head, "Left out: %s the owner said are not regular payments.\n", plural(rep.hidden, "payee", "payees"))
	}
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
			lines = append(lines, recurringLine(it, cur, rep.today))
			txns = append(txns, it.latest.transaction())
			if it.changed {
				highlight = append(highlight, it.latest.id)
			}
		}
	}
	if kind != "income" {
		section("Bills (confirmed by the owner):", g.bills)
		section("Subscriptions (confirmed by the owner):", g.subs)
	}
	if kind != "bills" {
		section("Income (confirmed by the owner):", g.income)
	}
	if kind != "income" {
		section("Not confirmed yet, going out: ask the owner what these are", g.unOut)
	}
	if kind != "bills" {
		section("Not confirmed yet, coming in: ask the owner what these are", g.unIn)
	}
	if len(lines) == 0 {
		head.WriteString("Nothing recurring to list: a payee needs at least 3 payments in a regular rhythm (2 for yearly).")
		return head.String(), nil, nil
	}
	return head.String() + fenced(lines), txns, highlight
}

// recurringLine describes one item. The payee is the bank's text, so the line sits in a wrapper.
func recurringLine(it recurring, currency string, today time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "id %s | %s | %s", it.id, oneLine(it.payee, 80), it.cadence.name)
	if it.label.kind == "" {
		if it.income {
			sb.WriteString(" | money in")
		} else {
			sb.WriteString(" | money out")
		}
	}
	if it.account != "" {
		fmt.Fprintf(&sb, " | account: %s", oneLine(it.account, 60))
	}
	if it.label.note != "" {
		fmt.Fprintf(&sb, " | the owner's note: %s", oneLine(it.label.note, maxNoteLen))
	}
	fmt.Fprintf(&sb, " | typical %s | last %s on %s", centsText(it.typical, currency), centsText(it.last, currency), it.lastDay.Format(time.DateOnly))
	if it.next.Before(today) {
		fmt.Fprintf(&sb, " | was expected %s and hasn't shown up yet", it.next.Format(time.DateOnly))
	} else {
		fmt.Fprintf(&sb, " | next expected about %s", it.next.Format(time.DateOnly))
	}
	if it.changed {
		fmt.Fprintf(&sb, " | CHANGED: from %s to %s", centsText(it.previous, currency), centsText(it.last, currency))
	} else if it.chSeen {
		fmt.Fprintf(&sb, " | changed from %s to %s on %s", centsText(it.chFrom, currency), centsText(it.chTo, currency), it.chDay.Format(time.DateOnly))
	}
	return sb.String()
}

func (s *Service) upcomingTool() agent.Tool {
	return agent.Tool{
		Name: "money_upcoming",
		Description: "Look ahead at the owner's cash flow over the next 30 days (up to 60): the checking balance after each expected " +
			"payment and payday, the lowest point, and whether checking covers what is due before the next payday. It plans only " +
			"around payments the owner has confirmed (see money_recurring and money_label_payee); payments that merely look regular " +
			"are listed apart, with what the lowest point would be if they also come. If nothing is confirmed yet, ask the owner " +
			"first. Use it for 'will I make it to payday?', 'can I afford this?' or 'what's coming up?'. These are estimates from " +
			"past patterns, not scheduled payments, and everyday spending (food, gas) isn't in them. Read-only. " + untrustedNote,
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
			// Transactions, balances and pending read as one picture: a sync
			// landing in between could count a payment twice or not at all.
			s.dataMu.RLock()
			rep, err := s.recurringReport(ctx)
			var accounts []Account
			var pending map[string]int64
			if err == nil {
				accounts, err = s.Accounts(ctx)
			}
			if err == nil {
				pending, err = s.pendingByAccount(ctx, rep.currency)
			}
			s.dataMu.RUnlock()
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{
				Content: describeUpcoming(rep, cashOnHand(accounts, rep.currency, pending), days),
				Stored:  keptNote("Looked ahead at", "upcoming payments and income"),
			}, nil
		}),
	}
}

// walk runs the occurrences against a starting balance: a line for each, the
// lowest balance and the day it is first reached ("" for the start), and the
// first day the balance is below zero.
func walk(start int64, occs []occurrence, currency string) (lines []string, low int64, lowDay, firstNeg string) {
	bal, low := start, start
	for _, o := range occs {
		// Income that is late may not come: it isn't counted until it arrives. A late
		// payment out still counts (the cautious way round).
		if !o.late || o.cents <= 0 {
			bal += o.cents
		}
		if bal < low {
			low, lowDay = bal, o.day.Format(time.DateOnly)
		}
		if bal < 0 && firstNeg == "" {
			firstNeg = o.day.Format(time.DateOnly)
		}
		lines = append(lines, upcomingLine(o, bal, true, currency))
	}
	return lines, low, lowDay, firstNeg
}

func within(occs []occurrence, end time.Time) []occurrence {
	var out []occurrence
	for _, o := range occs {
		if !o.day.After(end) {
			out = append(out, o)
		}
	}
	return out
}

func lowText(low int64, day, currency string) string {
	if day == "" {
		return fmt.Sprintf("right now, at %s", centsText(low, currency))
	}
	return fmt.Sprintf("%s on %s", centsText(low, currency), day)
}

// byAccount splits items by where they are charged or paid: a checking account,
// a credit card, or anything else (savings, loans).
func byAccount(list []recurring) (chk, card, other []recurring) {
	for _, it := range list {
		switch it.class {
		case acctChecking:
			chk = append(chk, it)
		case acctCard:
			card = append(card, it)
		default:
			other = append(other, it)
		}
	}
	return chk, card, other
}

// onChecking keeps the items charged to or paid into a checking account.
func onChecking(list []recurring) []recurring {
	chk, _, _ := byAccount(list)
	return chk
}

func kindCounts(list []recurring) (bills, subs, income int) {
	for _, it := range list {
		switch it.label.kind {
		case kindBill:
			bills++
		case kindSubscription:
			subs++
		case kindIncome:
			income++
		}
	}
	return bills, subs, income
}

// forAccount keeps the items on one account.
func forAccount(list []recurring, id string) []recurring {
	var out []recurring
	for _, it := range list {
		if it.accountID == id {
			out = append(out, it)
		}
	}
	return out
}

// forecast is one checking account's own look-ahead: its items run against its
// own balance.
type forecast struct {
	acct       checkingAcct
	lines      []string
	low        int64
	lowDay     string
	firstNeg   string
	due        int64 // its confirmed payments before the next payday
	moreBefore int64 // its unconfirmed payments before the next payday
	unConfN    int   // its unconfirmed payments out
	unConfSum  int64 // those that fall in the window
	ifLow      int64 // the lowest point if the unconfirmed ones also come
	ifDay      string
}

func forecastAccount(rep recurringReport, a checkingAcct, chk, unOut []recurring, end time.Time, payday *occurrence) forecast {
	cur := rep.currency
	mine, mineUn := forAccount(chk, a.id), forAccount(unOut, a.id)
	f := forecast{acct: a, unConfN: len(mineUn)}
	f.lines, f.low, f.lowDay, f.firstNeg = walk(a.cents, project(mine, rep.today, end), cur)
	for _, o := range project(mineUn, rep.today, end) {
		f.unConfSum -= o.cents
	}
	if f.unConfSum != 0 {
		_, f.ifLow, f.ifDay, _ = walk(a.cents, project(slices.Concat(mine, mineUn), rep.today, end), cur)
	}
	if payday != nil {
		for _, o := range project(mine, rep.today, payday.day) {
			if !o.income && o.day.Before(payday.day) {
				f.due -= o.cents
			}
		}
		for _, o := range project(mineUn, rep.today, payday.day) {
			if o.day.Before(payday.day) {
				f.moreBefore -= o.cents
			}
		}
	}
	return f
}

// findPayday is the next confirmed income into checking; with none confirmed, a
// detected deposit into checking stands in (unconfirmed true).
// findPayday is the next income into checking that is on time: a late one may not
// come, so bills aren't weighed against it.
func findPayday(rep recurringReport, confirmedAll []occurrence, unIn []recurring) (payday *occurrence, unconfirmed bool) {
	for i, o := range confirmedAll {
		if o.income && !o.late {
			return &confirmedAll[i], false
		}
	}
	cand := project(unIn, rep.today, rep.today.AddDate(0, 0, 124))
	for i, o := range cand {
		if !o.late {
			return &cand[i], true
		}
	}
	return nil, false
}

func whenText(p *occurrence, today time.Time) string {
	switch {
	case p.late:
		return "due now (expected earlier, not seen yet)"
	case p.day.Equal(today):
		return "today"
	}
	return p.day.Format(time.DateOnly)
}

func verdictText(balance, due int64, currency string) string {
	left := balance - due
	if left < 0 {
		return fmt.Sprintf("short by %s", centsText(-left, currency))
	}
	return fmt.Sprintf("leaving %s", centsText(left, currency))
}

// describeUpcoming projects the owner-confirmed items over the next days. Each
// checking account's items run against that account's own balance: another
// account's money can't cover a bill that comes out of this one. Card charges
// (which add to what is owed), items in savings or other accounts, and items that
// only look regular are listed apart.
func describeUpcoming(rep recurringReport, c cash, days int) string {
	cur := rep.currency
	end := rep.today.AddDate(0, 0, days)
	g := groupItems(rep.items)
	confirmed := g.confirmed()
	multi := len(c.accounts) > 1
	estimated := 0
	for _, a := range c.accounts {
		if a.estimated {
			estimated++
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Cash look-ahead for the next %d days (through %s). %s\n", days, end.Format(time.DateOnly), estimateNote)
	sb.WriteString("Everyday spending (food, gas, shopping) is not included, so the real balance will run lower.\n")
	switch {
	case c.checkingN == 0:
		sb.WriteString("No checking account balance is available, so running balances are skipped.")
	case multi:
		fmt.Fprintf(&sb, "Checking now: %d accounts, %s combined", c.checkingN, centsText(c.checking, cur))
		if estimated == 0 {
			sb.WriteString(" (available balances).")
		} else {
			fmt.Fprintf(&sb, " (%d of them have no available balance at the bank, so their figure is the current balance adjusted for pending payments and deposits: an estimate).", estimated)
		}
	case estimated > 0:
		fmt.Fprintf(&sb, "Checking now: %s (an estimate: the bank gave no available balance, so this is the current balance adjusted for pending payments and deposits).", centsText(c.checking, cur))
	default:
		fmt.Fprintf(&sb, "Checking now: %s (available balances).", centsText(c.checking, cur))
	}
	if c.savings != 0 {
		fmt.Fprintf(&sb, " Savings and other deposit accounts: %s.", centsText(c.savings, cur))
	}
	if c.cards > 0 {
		fmt.Fprintf(&sb, " Credit cards owe %s in total (%s).", centsText(c.owed, cur), plural(c.cards, "card", "cards"))
	}
	sb.WriteString("\n")
	if overdrawn := slices.IndexFunc(c.accounts, func(a checkingAcct) bool { return a.cents < 0 }); overdrawn >= 0 {
		if multi {
			n := 0
			for _, a := range c.accounts {
				if a.cents < 0 {
					n++
				}
			}
			fmt.Fprintf(&sb, "WARNING: %d of %d checking accounts are already below zero now, before anything ahead (details below).\n", n, len(c.accounts))
		} else {
			fmt.Fprintf(&sb, "WARNING: checking is already below zero now (%s), before anything ahead.\n", centsText(c.checking, cur))
		}
	}
	if len(rep.items) == 0 {
		sb.WriteString("No recurring payments or income were detected, so there is nothing to project.")
		return sb.String()
	}
	if len(confirmed) == 0 {
		fmt.Fprintf(&sb, "The owner hasn't confirmed any recurring payment or income yet (%s look regular), so there is nothing to plan around. "+
			"Ask them first: money_recurring lists them with ids, and money_label_payee records their answers.",
			plural(len(rep.items), "payment", "payments"))
		return sb.String()
	}

	chkAll, card, other := byAccount(confirmed)
	// Items on a checking account with no balance figure can't be run against one.
	known := map[string]bool{}
	for _, a := range c.accounts {
		known[a.id] = true
	}
	var chk []recurring
	for _, it := range chkAll {
		if known[it.accountID] {
			chk = append(chk, it)
		} else {
			other = append(other, it)
		}
	}
	unOutChk, unInChk := onChecking(g.unOut), onChecking(g.unIn)

	// Project past the window too, to find the next payday.
	all := project(chk, rep.today, end.AddDate(0, 0, 62))
	window := within(all, end)
	var out, in int64
	for _, o := range window {
		if o.income {
			if !o.late { // as in the running balance: late income isn't counted until it arrives
				in += o.cents
			}
		} else {
			out -= o.cents
		}
	}
	bills, subs, inc := kindCounts(chk)
	fmt.Fprintf(&sb, "Planned around what the owner confirmed on checking (%s, %s, %s). Expected in the window: payments out %s, income %s.\n",
		plural(bills, "bill", "bills"), plural(subs, "subscription", "subscriptions"), plural(inc, "income source", "income sources"),
		centsText(out, cur), centsText(in, cur))
	if len(card)+len(other) > 0 {
		fmt.Fprintf(&sb, "Confirmed items on a credit card (%d) or in savings or another account (%d) are listed apart: they don't move the checking balance.\n", len(card), len(other))
	}

	payday, unconfirmedPay := findPayday(rep, all, unInChk)
	var fcs []forecast
	for _, a := range c.accounts {
		fcs = append(fcs, forecastAccount(rep, a, chk, unOutChk, end, payday))
	}

	var body []string
	switch {
	case c.checkingN == 0:
		body = []string{"Confirmed by the owner, on checking:"}
		for _, o := range window {
			body = append(body, upcomingLine(o, 0, false, cur))
		}
		if len(window) == 0 {
			body[0] += " nothing falls in this window."
		}
	case !multi:
		f := fcs[0]
		fmt.Fprintf(&sb, "Lowest point: %s.\n", lowText(f.low, f.lowDay, cur))
		switch {
		case f.acct.cents < 0:
			// Already said above: only how low it goes from here.
			fmt.Fprintf(&sb, "On these estimates it goes as low as %s.\n", centsText(f.low, cur))
		case f.firstNeg != "":
			fmt.Fprintf(&sb, "WARNING: on these estimates checking dips below zero on %s (lowest %s) unless money is moved in.\n", f.firstNeg, centsText(f.low, cur))
		default:
			sb.WriteString("On these estimates checking stays above zero through the window.\n")
		}
		if payday == nil {
			sb.WriteString("No income into checking has been confirmed or detected, so there is no payday to compare against.\n")
		} else {
			label := "Next payday"
			if unconfirmedPay {
				label = "Next payday (a regular deposit the owner hasn't confirmed as income)"
			}
			fmt.Fprintf(&sb, "%s: %s. Confirmed payments from checking expected before then: %s, against %s in checking now: %s.\n",
				label, whenText(payday, rep.today), centsText(f.due, cur), centsText(f.acct.cents, cur), verdictText(f.acct.cents, f.due, cur))
			if f.moreBefore > 0 {
				fmt.Fprintf(&sb, "Unconfirmed payments out before then would add %s.\n", centsText(f.moreBefore, cur))
			}
		}
		if f.unConfSum != 0 {
			fmt.Fprintf(&sb, "Not confirmed yet (left out of the figures above): %s going out of checking, %s in the window. If these also come, the lowest point would be %s.\n",
				plural(f.unConfN, "payment", "payments"), centsText(f.unConfSum, cur), lowText(f.ifLow, f.ifDay, cur))
		}
		body = []string{"Confirmed by the owner, on checking:"}
		body = append(body, f.lines...)
		if len(f.lines) == 0 {
			body[0] += " nothing falls in this window."
		}
	default:
		short := 0
		for _, f := range fcs {
			if f.firstNeg != "" || f.acct.cents < 0 {
				short++
			}
		}
		sb.WriteString("Each checking account's own items run against its own balance (details per account below); another account's money doesn't cover them.\n")
		if short > 0 {
			fmt.Fprintf(&sb, "WARNING: %d of %d checking accounts would dip below zero on these estimates unless money is moved in from another account.\n", short, len(fcs))
		} else {
			sb.WriteString("On these estimates every checking account stays above zero through the window.\n")
		}
		switch {
		case payday == nil:
			sb.WriteString("No income into checking has been confirmed or detected, so there is no payday to compare against.\n")
		case unconfirmedPay:
			fmt.Fprintf(&sb, "Next payday: %s (a regular deposit the owner hasn't confirmed as income).\n", whenText(payday, rep.today))
		default:
			fmt.Fprintf(&sb, "Next payday: %s.\n", whenText(payday, rep.today))
		}
		if payday != nil {
			for _, a := range c.accounts {
				if a.id == payday.accountID {
					body = append(body, fmt.Sprintf("The next payday lands in %s.", oneLine(a.name, 60)))
				}
			}
		}
		for _, f := range fcs {
			name := oneLine(f.acct.name, 60)
			head := fmt.Sprintf("Checking account %s, now %s", name, centsText(f.acct.cents, cur))
			if f.acct.estimated {
				head += " (estimated from the current balance and pending)"
			}
			body = append(body, head+":")
			body = append(body, f.lines...)
			if len(f.lines) == 0 {
				body = append(body, "Nothing confirmed falls in this window.")
			}
			body = append(body, fmt.Sprintf("Lowest point of %s: %s.", name, lowText(f.low, f.lowDay, cur)))
			switch {
			case f.acct.cents < 0:
				body = append(body, fmt.Sprintf("WARNING: %s is already below zero now and goes as low as %s unless money is moved in from another account.", name, centsText(f.low, cur)))
			case f.firstNeg != "":
				body = append(body, fmt.Sprintf("WARNING: %s would dip below zero on %s (lowest %s) unless money is moved in from another account.", name, f.firstNeg, centsText(f.low, cur)))
			}
			if payday != nil {
				body = append(body, fmt.Sprintf("Before the next payday: confirmed payments out of %s %s, against %s in it now: %s.",
					name, centsText(f.due, cur), centsText(f.acct.cents, cur), verdictText(f.acct.cents, f.due, cur)))
				if f.moreBefore > 0 {
					body = append(body, fmt.Sprintf("Unconfirmed payments out of %s before then would add %s.", name, centsText(f.moreBefore, cur)))
				}
			}
			if f.unConfSum != 0 {
				body = append(body, fmt.Sprintf("Not confirmed yet, out of %s: %s in the window; if these also come, its lowest point would be %s.",
					name, centsText(f.unConfSum, cur), lowText(f.ifLow, f.ifDay, cur)))
			}
		}
	}
	if len(g.unIn) > 0 {
		fmt.Fprintf(&sb, "Unconfirmed regular deposits (%s) are not counted as income.\n", plural(len(g.unIn), "payee", "payees"))
	}
	if len(g.unOut)+len(g.unIn) > 0 {
		sb.WriteString(askNote + "\n")
	}
	apart := func(title string, list []recurring) {
		occs := project(list, rep.today, end)
		if len(occs) == 0 {
			return
		}
		body = append(body, title)
		for _, o := range occs {
			body = append(body, upcomingLine(o, 0, false, cur))
		}
	}
	apart("Charged to a card (adds to what you owe; not taken from checking, not in the balances above):", card)
	apart("In savings or another account (not part of the checking balance):", other)
	apart("Not confirmed yet (not in the balances above):", slices.Concat(g.unOut, g.unIn))
	sb.WriteString("Date | amount | what | checking after\n")
	sb.WriteString(fenced(body))
	return sb.String()
}

func upcomingLine(o occurrence, balance int64, showBalance bool, currency string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s | %s | %s: %s", o.day.Format(time.DateOnly), centsText(o.cents, currency), o.status, oneLine(o.payee, 80))
	switch {
	case o.late && o.cents > 0:
		sb.WriteString(" (expected earlier, not seen yet: not counted until it arrives)")
	case o.late:
		sb.WriteString(" (expected earlier, not seen yet)")
	}
	if showBalance {
		fmt.Fprintf(&sb, " | checking after: %s", centsText(balance, currency))
	} else if o.account != "" {
		fmt.Fprintf(&sb, " | on %s", oneLine(o.account, 60))
	}
	return sb.String()
}
