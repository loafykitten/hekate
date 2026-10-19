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

// Limits that keep any accepted rule cheap to evaluate: occurrences per day,
// steps walked per query, and how far back a series may start.
const (
	maxPerDay = 24
	maxSteps  = 500_000
)

var earliestStart = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

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
	if len(opt.Byeaster) > 0 {
		// A non-standard rrule-go extension it doesn't bounds-check: an out-of-range
		// value panics inside the iterator.
		return nil, fmt.Errorf("repeat rule %q uses BYEASTER, which isn't supported", rule)
	}
	if perDay := max(1, len(opt.Byhour)) * max(1, len(opt.Byminute)) * max(1, len(opt.Bysecond)); perDay > maxPerDay {
		return nil, fmt.Errorf("repeat rule %q fires %d times a day; at most %d are supported", rule, perDay, maxPerDay)
	}
	if start.Before(earliestStart) {
		return nil, fmt.Errorf("repeat rule %q starts too far in the past", rule)
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
	return r.walk(func(o time.Time) bool { return o.After(t) })
}

// First returns the first occurrence at or after t.
func (r *Rule) First(t time.Time) (time.Time, bool) {
	return r.walk(func(o time.Time) bool { return !o.Before(t) })
}

// walk returns the first occurrence for which done is true, stepping at most
// maxSteps occurrences so no rule can keep the caller (which may hold a lock)
// busy indefinitely. Running out of steps counts as the series having ended.
func (r *Rule) walk(done func(time.Time) bool) (o time.Time, ok bool) {
	// rrule-go can panic on rules it accepted; a stored rule must never take
	// the service down, so a panic counts as the series having ended.
	defer func() {
		if recover() != nil {
			o, ok = time.Time{}, false
		}
	}()
	next := r.r.Iterator()
	for range maxSteps {
		o, ok := next()
		if !ok {
			return time.Time{}, false
		}
		if done(o) {
			return o, true
		}
	}
	return time.Time{}, false
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
		parts[i] = "COUNT=" + strconv.Itoa(max(n-r.countBefore(next), 1))
	}
	return strings.Join(parts, ";")
}

// countBefore counts occurrences strictly before t, walking at most maxSteps.
func (r *Rule) countBefore(t time.Time) (n int) {
	defer func() { _ = recover() }() // see walk
	next := r.r.Iterator()
	for range maxSteps {
		o, ok := next()
		if !ok || !o.Before(t) {
			break
		}
		n++
	}
	return n
}
