package calendar

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Conventions used throughout this package:
//
//   - All-day events are floating dates. A date such as 2026-10-20 means that
//     calendar day wherever the viewer is, and is held as midnight in the
//     configured zone only so it can be compared with timed events.
//   - The end of an all-day event is EXCLUSIVE, exactly as in RFC 5545: a
//     one-day event on 2026-10-20 has start "2026-10-20" and end "2026-10-21"; a
//     three-day event from the 20th to the 22nd (inclusive) ends "2026-10-23".
//     The same convention applies to input and output, so reading an event and
//     writing it back is lossless.
//   - Timed events are exact instants. They are presented in the configured
//     zone as RFC 3339 with offset, and written as DTSTART;TZID=<zone name>.

const (
	dateLayout     = "2006-01-02"
	icalDateLayout = "20060102"
	icalTimeLayout = "20060102T150405"
	icalUTCLayout  = "20060102T150405Z"

	propCreatedByHekate = "X-HEKATE-CREATED"
	prodID              = "-//Hekate//Calendar//EN"

	// maxInstances caps how many instances of one series a single query yields.
	maxInstances = 1000
	maxSteps     = 500_000 // occurrences walked from DTSTART to the end of a range
	maxPerDay    = 24
)

// Event is the PROTOCOL.md Event shape: one non-recurring event or one
// instance of a recurring one.
type Event struct {
	UID             string  `json:"uid"`
	Instance        *string `json:"instance"` // null for a non-recurring event
	Title           string  `json:"title"`
	Start           string  `json:"start"` // RFC 3339, or a date for all-day events
	End             string  `json:"end"`   // all-day: exclusive date (see conventions above)
	AllDay          bool    `json:"all_day"`
	Location        string  `json:"location"`
	Notes           string  `json:"notes"`
	RRule           *string `json:"rrule"`
	CreatedByHekate bool    `json:"created_by_hekate"`
}

// occurrence is an Event with its instants, for sorting and filtering.
type occurrence struct {
	Event
	start, end time.Time
}

// resolver turns iCalendar date values into times.
type resolver struct {
	def  *time.Location
	vtzs map[string]*time.Location
}

func newResolver(cal *ical.Calendar, def *time.Location) *resolver {
	r := &resolver{def: def, vtzs: map[string]*time.Location{}}
	if cal == nil {
		return r
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			continue
		}
		id := rawText(c.Props.Get(ical.PropTimezoneID))
		if id == "" {
			continue
		}
		// Some clients name zones with their own ids and put the Olson name in
		// X-LIC-LOCATION.
		if p := c.Props.Get("X-LIC-LOCATION"); p != nil {
			if loc, err := time.LoadLocation(strings.TrimSpace(p.Value)); err == nil {
				r.vtzs[id] = loc
			}
		}
	}
	return r
}

// zone resolves a TZID. An id the host cannot resolve falls back to the
// configured zone, which is right for the usual single-user calendar.
func (r *resolver) zone(tzid string) *time.Location {
	if tzid == "" {
		return r.def
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	if loc, ok := r.vtzs[tzid]; ok {
		return loc
	}
	return r.def
}

// parseValue parses one date or date-time value of prop. It returns the
// location the value is expressed in, which recurrence expansion needs so that
// wall-clock times survive daylight saving changes.
func (r *resolver) parseValue(p *ical.Prop, value string) (t time.Time, allDay bool, loc *time.Location, err error) {
	value = strings.TrimSpace(value)
	loc = r.zone(p.Params.Get(ical.PropTimezoneID))
	isDate := ical.ValueType(strings.ToUpper(p.Params.Get(ical.ParamValue))) == ical.ValueDate || len(value) == len(icalDateLayout)
	switch {
	case isDate:
		t, err = time.ParseInLocation(icalDateLayout, value, r.def)
		return t, true, r.def, err
	case strings.HasSuffix(value, "Z"):
		t, err = time.ParseInLocation(icalUTCLayout, value, time.UTC)
		return t, false, time.UTC, err
	default:
		t, err = time.ParseInLocation(icalTimeLayout, value, loc)
		return t, false, loc, err
	}
}

func (r *resolver) parseProp(p *ical.Prop) (time.Time, bool, *time.Location, error) {
	return r.parseValue(p, p.Value)
}

// rawText reads a text property tolerantly: unknown escapes are kept and
// unescaped commas do not split the value (other clients write both).
func rawText(p *ical.Prop) string {
	if p == nil {
		return ""
	}
	var sb strings.Builder
	v := p.Value
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' || i+1 >= len(v) {
			sb.WriteByte(c)
			continue
		}
		i++
		switch v[i] {
		case 'n', 'N':
			sb.WriteByte('\n')
		default:
			sb.WriteByte(v[i])
		}
	}
	return sb.String()
}

// component is one parsed VEVENT.
type component struct {
	c          *ical.Component
	start, end time.Time
	allDay     bool
	loc        *time.Location // zone DTSTART is expressed in
	recID      time.Time
	hasRecID   bool
	cancelled  bool
}

func (r *resolver) parseComponent(c *ical.Component) (component, error) {
	out := component{c: c}
	sp := c.Props.Get(ical.PropDateTimeStart)
	if sp == nil {
		return out, errors.New("event has no DTSTART")
	}
	var err error
	out.start, out.allDay, out.loc, err = r.parseProp(sp)
	if err != nil {
		return out, fmt.Errorf("DTSTART %q: %w", sp.Value, err)
	}
	switch {
	case c.Props.Get(ical.PropDateTimeEnd) != nil:
		ep := c.Props.Get(ical.PropDateTimeEnd)
		end, endAllDay, _, err := r.parseProp(ep)
		if err != nil {
			return out, fmt.Errorf("DTEND %q: %w", ep.Value, err)
		}
		if endAllDay != out.allDay && endAllDay {
			// A date DTEND on a timed DTSTART: take it as the start of that day.
			end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, out.loc)
		}
		out.end = end
	case c.Props.Get(ical.PropDuration) != nil:
		d, err := c.Props.Get(ical.PropDuration).Duration()
		if err != nil {
			return out, fmt.Errorf("DURATION: %w", err)
		}
		if out.allDay {
			out.end = out.start.AddDate(0, 0, int((d+12*time.Hour)/(24*time.Hour)))
		} else {
			out.end = out.start.Add(d)
		}
	case out.allDay:
		out.end = out.start.AddDate(0, 0, 1)
	default:
		out.end = out.start
	}
	if out.end.Before(out.start) {
		out.end = out.start
	}
	if rp := c.Props.Get(ical.PropRecurrenceID); rp != nil {
		out.recID, _, _, err = r.parseProp(rp)
		if err != nil {
			return out, fmt.Errorf("RECURRENCE-ID %q: %w", rp.Value, err)
		}
		out.hasRecID = true
	}
	out.cancelled = strings.EqualFold(rawText(c.Props.Get(ical.PropStatus)), "CANCELLED")
	return out, nil
}

// series is everything stored under one UID: the master VEVENT and any
// RECURRENCE-ID overrides.
type series struct {
	cal       *ical.Calendar
	uid       string
	master    *component
	overrides []component
	res       *resolver
}

var errNoEvent = errors.New("no event in calendar object")

// parseSeries splits a calendar object into its master and overrides. A
// component that cannot be parsed is skipped; the error reports the first.
func parseSeries(cal *ical.Calendar, def *time.Location) (*series, error) {
	s := &series{cal: cal, res: newResolver(cal, def)}
	var firstErr error
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		pc, err := s.res.parseComponent(c)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if s.uid == "" {
			s.uid = rawText(c.Props.Get(ical.PropUID))
		}
		if pc.hasRecID {
			s.overrides = append(s.overrides, pc)
		} else if s.master == nil {
			cp := pc
			s.master = &cp
		}
	}
	if s.master == nil && len(s.overrides) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, errNoEvent
	}
	return s, nil
}

func (s *series) createdByHekate() bool {
	if s.master == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(rawText(s.master.c.Props.Get(propCreatedByHekate))), "TRUE")
}

// rruleString returns the master's RRULE value, or "".
func (s *series) rruleString() string {
	if s.master == nil {
		return ""
	}
	if p := s.master.c.Props.Get(ical.PropRecurrenceRule); p != nil {
		return strings.TrimSpace(p.Value)
	}
	return ""
}

func (s *series) event(c component, loc *time.Location, instance *string) Event {
	ev := Event{
		UID:             s.uid,
		Instance:        instance,
		Title:           rawText(c.c.Props.Get(ical.PropSummary)),
		Start:           formatTime(c.start, c.allDay, loc),
		End:             formatTime(c.end, c.allDay, loc),
		AllDay:          c.allDay,
		Location:        rawText(c.c.Props.Get(ical.PropLocation)),
		Notes:           rawText(c.c.Props.Get(ical.PropDescription)),
		CreatedByHekate: s.createdByHekate(),
	}
	if rr := s.rruleString(); rr != "" {
		ev.RRule = &rr
	}
	return ev
}

func formatTime(t time.Time, allDay bool, loc *time.Location) string {
	if allDay {
		return t.Format(dateLayout)
	}
	return t.In(loc).Format(time.RFC3339)
}

func overlaps(start, end, from, to time.Time) bool {
	if end.After(start) {
		return start.Before(to) && end.After(from)
	}
	return !start.Before(from) && start.Before(to)
}

// occurrences expands the series into instances overlapping [from, to).
func (s *series) occurrences(from, to time.Time, loc *time.Location) ([]occurrence, error) {
	var out []occurrence
	overridden := map[int64]bool{}
	for _, o := range s.overrides {
		overridden[o.recID.Unix()] = true
	}

	if m := s.master; m != nil && !m.cancelled {
		starts, err := s.masterStarts(from, to)
		if err != nil {
			return nil, err
		}
		recurring := s.rruleString() != "" || len(m.c.Props.Values(ical.PropRecurrenceDates)) > 0
		for _, st := range starts {
			if overridden[st.Unix()] {
				continue
			}
			c := *m
			c.start = st
			if m.allDay {
				c.end = st.AddDate(0, 0, daysBetween(m.start, m.end))
			} else {
				c.end = st.Add(m.end.Sub(m.start))
			}
			if !overlaps(c.start, c.end, from, to) {
				continue
			}
			var inst *string
			if recurring {
				v := formatTime(st, m.allDay, loc)
				inst = &v
			}
			out = append(out, occurrence{Event: s.event(c, loc, inst), start: c.start, end: c.end})
		}
	}

	for _, o := range s.overrides {
		if o.cancelled || !overlaps(o.start, o.end, from, to) {
			continue
		}
		v := formatTime(o.recID, o.allDay, loc)
		out = append(out, occurrence{Event: s.event(o, loc, &v), start: o.start, end: o.end})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}

func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Round(24*time.Hour) / (24 * time.Hour))
}

// masterStarts lists the start of every instance of the master that could
// overlap [from, to), after applying RRULE, RDATE and EXDATE. The result may
// include instances that end up not overlapping; the caller filters.
func (s *series) masterStarts(from, to time.Time) ([]time.Time, error) {
	m := s.master
	dur := m.end.Sub(m.start)
	// Look back by the event length plus a day so an instance that began before
	// the window but is still running is found, whatever the zone offsets.
	lo := from.Add(-dur - 25*time.Hour)

	seen := map[int64]bool{}
	var starts []time.Time
	add := func(t time.Time) {
		if k := t.Unix(); !seen[k] {
			seen[k] = true
			starts = append(starts, t)
		}
	}

	// DTSTART is always the first instance (RFC 5545 section 3.8.5.3).
	add(m.start)

	if rp := m.c.Props.Get(ical.PropRecurrenceRule); rp != nil {
		opt, err := rrule.StrToROptionInLocation(strings.TrimSpace(rp.Value), m.loc)
		if err != nil {
			return nil, fmt.Errorf("RRULE %q: %w", rp.Value, err)
		}
		if err := checkRuleDensity(opt); err != nil {
			return nil, fmt.Errorf("RRULE %q: %w", rp.Value, err)
		}
		opt.Dtstart = m.start
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, fmt.Errorf("RRULE %q: %w", rp.Value, err)
		}
		// Iterate lazily: Between would build every occurrence before any limit
		// applied. Both the kept instances and the steps to reach the range are
		// capped, so no stored rule can run away with memory or time.
		next := rule.Iterator()
		for n, steps := 0, 0; n < maxInstances && steps < maxSteps; steps++ {
			t, ok := next()
			if !ok || t.After(to) {
				break
			}
			if !t.Before(lo) {
				add(t)
				n++
			}
		}
	}

	for _, p := range m.c.Props.Values(ical.PropRecurrenceDates) {
		for _, v := range strings.Split(p.Value, ",") {
			if strings.Contains(v, "/") {
				continue // PERIOD values are not supported
			}
			t, _, _, err := s.res.parseValue(&p, v)
			if err != nil {
				return nil, fmt.Errorf("RDATE %q: %w", v, err)
			}
			add(t)
		}
	}

	excluded := map[int64]bool{}
	for _, p := range m.c.Props.Values(ical.PropExceptionDates) {
		for _, v := range strings.Split(p.Value, ",") {
			t, allDay, _, err := s.res.parseValue(&p, v)
			if err != nil {
				return nil, fmt.Errorf("EXDATE %q: %w", v, err)
			}
			if allDay && !m.allDay {
				// A date on a timed series removes every instance that day.
				for _, st := range starts {
					if st.In(m.loc).Format(dateLayout) == t.Format(dateLayout) {
						excluded[st.Unix()] = true
					}
				}
				continue
			}
			excluded[t.Unix()] = true
		}
	}

	out := starts[:0]
	for _, st := range starts {
		if !excluded[st.Unix()] {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

// --- building and editing ---

// spec is a fully resolved event to write.
type spec struct {
	title, location, notes string
	start, end             time.Time
	allDay                 bool
	rrule                  string // "" for none
}

// zoneName is the TZID to write for loc, or "" when times must be written in
// UTC because the zone has no portable name.
func zoneName(loc *time.Location) string {
	switch n := loc.String(); n {
	case "", "Local", "UTC":
		return ""
	default:
		return n
	}
}

func setTime(c *ical.Component, name string, t time.Time, allDay bool, loc *time.Location) {
	p := ical.NewProp(name)
	switch {
	case allDay:
		p.Params.Set(ical.ParamValue, string(ical.ValueDate))
		p.Value = t.Format(icalDateLayout)
	case zoneName(loc) == "":
		p.Value = t.UTC().Format(icalUTCLayout)
	default:
		p.Params.Set(ical.PropTimezoneID, zoneName(loc))
		p.Value = t.In(loc).Format(icalTimeLayout)
	}
	c.Props.Set(p)
}

func setText(c *ical.Component, name, v string) {
	if v == "" {
		c.Props.Del(name)
		return
	}
	c.Props.SetText(name, v)
}

func setUTC(c *ical.Component, name string, t time.Time) {
	p := ical.NewProp(name)
	p.Value = t.UTC().Format(icalUTCLayout)
	c.Props.Set(p)
}

// fillEvent writes sp into the VEVENT, leaving properties it does not manage
// (alarms, attendees, extensions from other clients) alone.
func fillEvent(c *ical.Component, sp spec, loc *time.Location, now time.Time) {
	setText(c, ical.PropSummary, sp.title)
	setText(c, ical.PropLocation, sp.location)
	setText(c, ical.PropDescription, sp.notes)
	setTime(c, ical.PropDateTimeStart, sp.start, sp.allDay, loc)
	c.Props.Del(ical.PropDuration)
	setTime(c, ical.PropDateTimeEnd, sp.end, sp.allDay, loc)
	if sp.rrule == "" {
		c.Props.Del(ical.PropRecurrenceRule)
	} else {
		p := ical.NewProp(ical.PropRecurrenceRule)
		p.Value = sp.rrule
		c.Props.Set(p)
	}
	setUTC(c, ical.PropDateTimeStamp, now)
	setUTC(c, ical.PropLastModified, now)
}

func newCalendarObject(uid string, sp spec, loc *time.Location, now time.Time) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, prodID)
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	setUTC(ev.Component, ical.PropCreated, now)
	fillEvent(ev.Component, sp, loc, now)
	flag := ical.NewProp(propCreatedByHekate)
	flag.Value = "TRUE" // a plain value; SetText would add VALUE=TEXT
	ev.Props.Set(flag)
	cal.Children = append(cal.Children, ev.Component)
	return cal
}

// applyUpdate rewrites the master of s with sp. Rescheduling a series (a new
// start, all-day flag or rule) drops its one-off exceptions, because their
// dates no longer line up; plain edits keep them.
func (s *series) applyUpdate(sp spec, resched bool, loc *time.Location, now time.Time) error {
	m := s.master
	if m == nil {
		return errors.New("this event has no main entry to edit")
	}
	// What an edit doesn't change stays exactly as written, including its time
	// zone: rewriting another client's recurring event in our zone would move
	// it across DST changes. With the start kept, a new end is written in the
	// start's own zone.
	keepStart := !resched
	keepEnd := keepStart && sp.end.Equal(m.end)
	saved := map[string][]ical.Prop{}
	if keepStart {
		saved[ical.PropDateTimeStart] = m.c.Props[ical.PropDateTimeStart]
		saved[ical.PropRecurrenceRule] = m.c.Props[ical.PropRecurrenceRule]
	}
	if keepEnd {
		saved[ical.PropDateTimeEnd] = m.c.Props[ical.PropDateTimeEnd]
		saved[ical.PropDuration] = m.c.Props[ical.PropDuration]
	}
	fillEvent(m.c, sp, loc, now)
	for name, props := range saved {
		if props == nil {
			delete(m.c.Props, name)
		} else {
			m.c.Props[name] = props
		}
	}
	if keepStart && !keepEnd && m.loc != nil {
		setTime(m.c, ical.PropDateTimeEnd, sp.end, sp.allDay, m.loc)
	}
	seq := 0
	if p := m.c.Props.Get(ical.PropSequence); p != nil {
		_, _ = fmt.Sscanf(p.Value, "%d", &seq)
	}
	seqProp := ical.NewProp(ical.PropSequence)
	seqProp.Value = fmt.Sprint(seq + 1) // an integer, so not SetText
	m.c.Props.Set(seqProp)
	if resched {
		m.c.Props.Del(ical.PropExceptionDates)
		m.c.Props.Del(ical.PropRecurrenceDates)
		kept := s.cal.Children[:0]
		for _, c := range s.cal.Children {
			if c.Name == ical.CompEvent && c.Props.Get(ical.PropRecurrenceID) != nil {
				continue
			}
			kept = append(kept, c)
		}
		s.cal.Children = kept
		s.overrides = nil
	}
	return nil
}

// validateRRule checks a rule the caller supplied and returns it in canonical
// form (no "RRULE:" prefix).
func validateRRule(v string) (string, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "RRULE:"), "rrule:")
	if v == "" {
		return "", nil
	}
	opt, err := rrule.StrToROption(v)
	if err != nil {
		return "", fmt.Errorf("invalid rrule %q: %w", v, err)
	}
	if err := checkRuleDensity(opt); err != nil {
		return "", fmt.Errorf("rrule %q: %w", v, err)
	}
	return v, nil
}

// checkRuleDensity rejects rules that repeat more often than daily, or that use
// BYHOUR/BYMINUTE/BYSECOND lists to pack more than maxPerDay times into a day.
func checkRuleDensity(opt *rrule.ROption) error {
	if opt.Freq > rrule.DAILY {
		return errors.New("repeating more often than daily is not supported")
	}
	perDay := max(1, len(opt.Byhour)) * max(1, len(opt.Byminute)) * max(1, len(opt.Bysecond))
	if perDay > maxPerDay {
		return fmt.Errorf("%d times a day is more than the %d supported", perDay, maxPerDay)
	}
	return nil
}
