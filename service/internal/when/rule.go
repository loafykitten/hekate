package when

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// maxRuleLen bounds what we accept and store.
const maxRuleLen = 300

// Rule is an RFC 5545 RRULE anchored at a start time and expanded in a zone,
// so "every Monday at 09:00" stays at 09:00 across daylight-saving changes.
type Rule struct {
	r    *rrule.RRule
	text string
}

// ParseRule reads an RRULE such as "FREQ=WEEKLY;BYDAY=MO" (an "RRULE:" prefix is
// tolerated) starting at start, expanded in loc. Sub-hourly rules are refused:
// they have no use here and would make catch-up loops long.
func ParseRule(rule string, start time.Time, loc *time.Location) (*Rule, error) {
	rule = strings.ToUpper(strings.TrimSpace(rule)) // keyword case is free in RFC 5545
	rule = strings.TrimPrefix(rule, "RRULE:")
	if rule == "" || len(rule) > maxRuleLen || strings.ContainsAny(rule, "\r\n") {
		return nil, errors.New("repeat rule must be a single RRULE such as FREQ=WEEKLY;BYDAY=MO")
	}
	if strings.Contains(rule, "DTSTART") {
		return nil, errors.New("repeat rule must not contain DTSTART; the start comes from the due time")
	}
	opt, err := rrule.StrToROptionInLocation(rule, loc)
	if err != nil {
		return nil, fmt.Errorf("repeat rule %q is not valid: %w", rule, err)
	}
	if opt.Freq > rrule.HOURLY { // MINUTELY and SECONDLY sort after HOURLY
		return nil, fmt.Errorf("repeat rule %q repeats too often; hourly is the shortest", rule)
	}
	// An hourly rule filtered by hour, minute or second can step past every
	// match forever (FREQ=HOURLY;INTERVAL=2;BYHOUR=1 from midnight never lands
	// on 1:00), and rrule-go can't be interrupted. Hourly rules may only be
	// narrowed by day; for times of day, use a daily rule with BYHOUR.
	if opt.Freq == rrule.HOURLY && (len(opt.Byhour) > 0 || len(opt.Byminute) > 0 || len(opt.Bysecond) > 0) {
		return nil, fmt.Errorf("repeat rule %q can't combine hourly repeats with BYHOUR, BYMINUTE or BYSECOND; use FREQ=DAILY with BYHOUR for times of day", rule)
	}
	opt.Dtstart = start.In(loc)
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, fmt.Errorf("repeat rule %q is not valid: %w", rule, err)
	}
	return &Rule{r: r, text: rule}, nil
}

// String is the rule in canonical form (upper case, no RRULE: prefix), the form to store.
func (r *Rule) String() string { return r.text }

// Next returns the first occurrence strictly after t, and false when the rule
// has ended (COUNT or UNTIL reached).
func (r *Rule) Next(t time.Time) (time.Time, bool) {
	n := r.r.After(t, false)
	return n, !n.IsZero()
}

// First returns the first occurrence at or after t.
func (r *Rule) First(t time.Time) (time.Time, bool) {
	n := r.r.After(t, true)
	return n, !n.IsZero()
}

// Rebase returns the rule text to store once the series has moved on to the
// occurrence next. A COUNT limit is spent as occurrences pass: the rule is
// re-anchored at next, where COUNT would otherwise start over.
func (r *Rule) Rebase(next time.Time) string {
	parts := strings.Split(r.text, ";")
	for i, p := range parts {
		v, ok := strings.CutPrefix(p, "COUNT=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			break
		}
		spent := len(r.r.Between(r.r.GetDTStart(), next, true)) - 1 // occurrences before next
		parts[i] = "COUNT=" + strconv.Itoa(max(n-spent, 1))
	}
	return strings.Join(parts, ";")
}
