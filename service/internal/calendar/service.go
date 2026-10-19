// Package calendar is Hekate's view of the owner's calendar: a CalDAV client
// (Radicale on the homelab), expansion of recurring events, the agent tools
// and the REST routes the menu uses. The calendar server stays the source of
// truth; nothing is cached except short-lived prompt text.
package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/google/uuid"

	"github.com/loafykitten/hekate/service/internal/config"
)

// Errors returned by Service.
var (
	// ErrNotConfigured means no CalDAV server is set up.
	ErrNotConfigured = errors.New("the calendar is not configured")
	// ErrNotFound means no event has that uid.
	ErrNotFound = errNotFound
	// ErrNotOwned means the event was not created by Hekate and the caller
	// asked to touch only Hekate's own events.
	ErrNotOwned = errors.New("this event was not created by Hekate")
)

// InputError is a problem with what the caller supplied (HTTP 400).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func badInput(format string, a ...any) error { return &InputError{Msg: fmt.Sprintf(format, a...)} }

// Options are the dependencies of a Service.
type Options struct {
	URL          string // CalDAV base URL; empty means not configured
	Username     string
	Password     config.Secret
	CalendarName string // display name of the calendar; created when missing. Default "Calendar".
	Location     *time.Location
	Logger       *slog.Logger
	// HTTPClient overrides the client used to reach the server (tests).
	HTTPClient *http.Client
	// OnChange is called with "calendar" after every mutation.
	OnChange func(domain string)
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Service reads and writes the calendar.
type Service struct {
	dav      *davClient // nil when unconfigured
	name     string
	loc      *time.Location
	log      *slog.Logger
	onChange func(string)
	now      func() time.Time

	promptMu    sync.Mutex
	promptText  string
	promptKey   string
	promptStamp time.Time
}

// New builds a Service. With an empty URL the service reports itself as
// unconfigured; with a bad URL it logs and does the same.
func New(o Options) *Service {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		name:     o.CalendarName,
		loc:      o.Location,
		log:      log,
		onChange: o.OnChange,
		now:      o.Now,
	}
	if s.name == "" {
		s.name = "Calendar"
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.now == nil {
		s.now = time.Now
	}
	if o.URL != "" {
		dav, err := newDAVClient(o.URL, o.Username, o.Password, s.name, o.HTTPClient, log)
		if err != nil {
			log.Error("calendar disabled", "err", err)
		} else {
			s.dav = dav
		}
	}
	return s
}

// Configured reports whether a CalDAV server is set up.
func (s *Service) Configured() bool { return s.dav != nil }

// CalendarName is the display name of the calendar in use.
func (s *Service) CalendarName() string { return s.name }

func (s *Service) changed() {
	s.promptMu.Lock()
	s.promptKey = ""
	s.promptMu.Unlock()
	if s.onChange != nil {
		s.onChange("calendar")
	}
}

// maxRange is the widest window one query may cover.
const maxRange = 400 * 24 * time.Hour

// Events lists events overlapping [from, to), with recurrences expanded.
func (s *Service) Events(ctx context.Context, from, to time.Time) ([]Event, error) {
	occ, err := s.occurrences(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]Event, len(occ))
	for i, o := range occ {
		out[i] = o.Event
	}
	return out, nil
}

func (s *Service) occurrences(ctx context.Context, from, to time.Time) ([]occurrence, error) {
	if s.dav == nil {
		return nil, ErrNotConfigured
	}
	if !to.After(from) {
		return nil, badInput("the end of the range must be after its start")
	}
	if to.Sub(from) > maxRange {
		return nil, badInput("the range is too long; ask for at most 400 days")
	}
	objs, err := s.dav.query(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out := []occurrence{}
	for _, o := range objs {
		ser, err := parseSeries(o.cal, s.loc)
		if err != nil {
			s.log.Warn("skipping unreadable calendar object", "path", o.path, "err", err)
			continue
		}
		occ, err := ser.occurrences(from, to, s.loc)
		if err != nil {
			s.log.Warn("skipping event that cannot be expanded", "uid", ser.uid, "err", err)
			continue
		}
		for i := range occ {
			occ[i].Version = etagValue(o.etag)
		}
		out = append(out, occ...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].start.Equal(out[j].start) {
			return out[i].start.Before(out[j].start)
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

// Get returns the main entry of the event with uid.
func (s *Service) Get(ctx context.Context, uid string) (Event, error) {
	if s.dav == nil {
		return Event{}, ErrNotConfigured
	}
	o, err := s.dav.find(ctx, uid)
	if err != nil {
		return Event{}, err
	}
	ser, err := parseSeries(o.cal, s.loc)
	if err != nil {
		return Event{}, err
	}
	ev := s.mainEvent(ser)
	ev.Version = etagValue(o.etag)
	return ev, nil
}

// mainEvent is the event as one record: for a repeating event, its first
// instance with the rule.
func (s *Service) mainEvent(ser *series) Event {
	c := ser.master
	if c == nil {
		c = &ser.overrides[0]
	}
	var inst *string
	if ser.rruleString() != "" {
		v := formatTime(c.start, c.allDay, s.loc)
		inst = &v
	}
	return ser.event(*c, s.loc, inst)
}

// EventInput is the body of a create request.
type EventInput struct {
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end,omitempty"`
	AllDay   *bool  `json:"all_day,omitempty"`
	Location string `json:"location,omitempty"`
	Notes    string `json:"notes,omitempty"`
	RRule    string `json:"rrule,omitempty"`
}

// Create adds an event and returns it.
func (s *Service) Create(ctx context.Context, in EventInput) (Event, error) {
	if s.dav == nil {
		return Event{}, ErrNotConfigured
	}
	sp, err := resolveSpec(in, s.loc)
	if err != nil {
		return Event{}, err
	}
	col, err := s.dav.collection(ctx)
	if err != nil {
		return Event{}, err
	}
	uid := uuid.NewString()
	cal := newCalendarObject(uid, sp, s.loc, s.now())
	etag, err := s.dav.put(ctx, objectPath(col, uid), cal, "")
	if err != nil {
		return Event{}, err
	}
	s.changed()
	ser, err := parseSeries(cal, s.loc)
	if err != nil {
		return Event{}, err
	}
	ev := s.mainEvent(ser)
	ev.Version = etagValue(etag)
	return ev, nil
}

// EventPatch is the body of an update request. Fields left out keep their
// value. The read-only fields of Event are accepted and ignored so a client
// can send back what it read.
type EventPatch struct {
	Title    *string         `json:"title"`
	Start    *string         `json:"start"`
	End      *string         `json:"end"`
	AllDay   *bool           `json:"all_day"`
	Location *string         `json:"location"`
	Notes    *string         `json:"notes"`
	RRule    json.RawMessage `json:"rrule"` // absent: keep; null or "": remove the rule

	UID      json.RawMessage `json:"uid"`
	Instance json.RawMessage `json:"instance"`
	// Version: the event's version when the edit was made; a newer stored
	// version refuses the edit with a conflict.
	Version         *string         `json:"version"`
	CreatedByHekate json.RawMessage `json:"created_by_hekate"`
}

// rrulePatch reports whether the patch touches the rule, and its new value.
func (p EventPatch) rrulePatch() (set bool, value string, err error) {
	raw := strings.TrimSpace(string(p.RRule))
	switch raw {
	case "":
		return false, "", nil
	case "null":
		return true, "", nil
	}
	if err := json.Unmarshal(p.RRule, &value); err != nil {
		return false, "", badInput("rrule must be a string or null")
	}
	return true, value, nil
}

// Update changes an event (the whole series for a repeating one). With
// onlyHekate it refuses events Hekate did not create.
func (s *Service) Update(ctx context.Context, uid string, p EventPatch, onlyHekate bool) (Event, error) {
	var result Event
	etag, err := s.mutate(ctx, uid, func(o *object, ser *series) (bool, error) {
		if ser.master == nil {
			return false, badInput("this event has no main entry to edit")
		}
		if onlyHekate && !ser.createdByHekate() {
			return false, ErrNotOwned
		}
		// An edit made against an older version of the event (another editor
		// saved in between) is refused rather than applied on top of it.
		if p.Version != nil && *p.Version != "" && *p.Version != etagValue(o.etag) {
			return false, errConflict
		}
		cur := ser.event(*ser.master, s.loc, nil)
		// A copy per attempt: mutate retries this after an ETag conflict, and the
		// translation must start from the caller's patch each time.
		patch := p
		if err := anchorToSeries(&patch, ser.master, ser.rruleString(), s.loc); err != nil {
			return false, err
		}
		in, err := mergePatch(cur, ser.master, patch, s.loc)
		if err != nil {
			return false, err
		}
		sp, err := resolveSpec(in, s.loc)
		if err != nil {
			return false, err
		}
		zone := s.loc // membership is a property of the series, in its own zone
		if ser.master.loc != nil {
			zone = ser.master.loc
		}
		if err := checkMoveAgainstRule(ser.master.start, sp.start, sp.rrule, zone); err != nil {
			return false, err
		}
		resched := !sp.start.Equal(ser.master.start) || sp.allDay != ser.master.allDay || sp.rrule != ser.rruleString()
		if err := ser.applyUpdate(sp, resched, s.loc, s.now()); err != nil {
			return false, err
		}
		fresh, err := parseSeries(o.cal, s.loc)
		if err != nil {
			return false, err
		}
		result = s.mainEvent(fresh)
		return true, nil
	})
	if err != nil {
		return Event{}, err
	}
	result.Version = etagValue(etag)
	return result, nil
}

// Delete removes an event and, for a repeating one, the whole series.
func (s *Service) Delete(ctx context.Context, uid string, onlyHekate bool) error {
	if s.dav == nil {
		return ErrNotConfigured
	}
	for attempt := 0; ; attempt++ {
		o, err := s.dav.find(ctx, uid)
		if err != nil {
			return err
		}
		if onlyHekate {
			ser, err := parseSeries(o.cal, s.loc)
			if err != nil {
				return err
			}
			if !ser.createdByHekate() {
				return ErrNotOwned
			}
		}
		err = s.dav.remove(ctx, o.path, o.etag)
		if errors.Is(err, errConflict) && attempt == 0 {
			continue
		}
		if err != nil {
			return err
		}
		s.changed()
		return nil
	}
}

// mutate finds the event, lets fn edit it and saves with If-Match. When the
// event changed on the server in between (412) it is fetched once more and
// the edit is applied again.
func (s *Service) mutate(ctx context.Context, uid string, fn func(*object, *series) (bool, error)) (etag string, err error) {
	if s.dav == nil {
		return "", ErrNotConfigured
	}
	for attempt := 0; ; attempt++ {
		o, err := s.dav.find(ctx, uid)
		if err != nil {
			return "", err
		}
		ser, err := parseSeries(o.cal, s.loc)
		if err != nil {
			return "", err
		}
		write, err := fn(o, ser)
		if err != nil {
			return "", err
		}
		if !write {
			return o.etag, nil
		}
		etag, err := s.dav.put(ctx, o.path, o.cal, o.etag)
		if errors.Is(err, errConflict) && attempt == 0 {
			continue
		}
		if err != nil {
			if errors.Is(err, errConflict) {
				return "", fmt.Errorf("%w; try again", errConflict)
			}
			return "", err
		}
		s.changed()
		return etag, nil
	}
}

// --- input resolution ---

const (
	maxTitle    = 300
	maxLocation = 500
	maxNotes    = 8000
)

// parseInput reads a time in one of the accepted forms: RFC 3339, or a local
// time or date in loc.
func parseInput(v string, loc *time.Location) (t time.Time, dateOnly bool, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false, badInput("a time is missing")
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.In(loc), false, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, layout == "2006-01-02", nil
		}
	}
	return time.Time{}, false, badInput("cannot read %q as a time; use 2026-10-20T15:00, 2026-10-20 or RFC 3339", v)
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// resolveSpec validates an input and works out the concrete times.
func resolveSpec(in EventInput, loc *time.Location) (spec, error) {
	title := strings.TrimSpace(in.Title)
	switch {
	case title == "":
		return spec{}, badInput("title is required")
	case len([]rune(title)) > maxTitle:
		return spec{}, badInput("title is too long (at most %d characters)", maxTitle)
	case len([]rune(in.Location)) > maxLocation:
		return spec{}, badInput("location is too long (at most %d characters)", maxLocation)
	case len([]rune(in.Notes)) > maxNotes:
		return spec{}, badInput("notes are too long (at most %d characters)", maxNotes)
	}
	start, dateOnly, err := parseInput(in.Start, loc)
	if err != nil {
		return spec{}, fmt.Errorf("start: %w", err)
	}
	allDay := dateOnly
	if in.AllDay != nil {
		allDay = *in.AllDay
	}
	if !allDay && dateOnly {
		return spec{}, badInput("start needs a time of day (or set all_day)")
	}
	rr, err := validateRRule(in.RRule)
	if err != nil {
		return spec{}, badInput("%v", err)
	}
	sp := spec{
		title:    title,
		location: strings.TrimSpace(in.Location),
		notes:    strings.TrimSpace(in.Notes),
		allDay:   allDay,
		rrule:    rr,
	}
	if allDay {
		sp.start = dayStart(start, loc)
		sp.end = sp.start.AddDate(0, 0, 1)
		if strings.TrimSpace(in.End) != "" {
			end, _, err := parseInput(in.End, loc)
			if err != nil {
				return spec{}, fmt.Errorf("end: %w", err)
			}
			end = dayStart(end, loc)
			switch {
			case end.Before(sp.start):
				return spec{}, badInput("end is before start")
			case end.After(sp.start):
				sp.end = end
			}
			// end == start: one day, so a caller who thinks of the end as
			// inclusive still gets the day they meant.
		}
		return sp, nil
	}
	sp.start = start
	sp.end = start.Add(time.Hour)
	if strings.TrimSpace(in.End) != "" {
		end, endDateOnly, err := parseInput(in.End, loc)
		if err != nil {
			return spec{}, fmt.Errorf("end: %w", err)
		}
		if endDateOnly {
			return spec{}, badInput("end needs a time of day for a timed event")
		}
		if !end.After(start) {
			return spec{}, badInput("end must be after start")
		}
		sp.end = end
	}
	return sp, nil
}

// mergePatch overlays p on the current event, returning a complete input. When
// the start moves and no end is given, the length of the event is kept.
// anchorToSeries rewrites a patch made while looking at one occurrence of a
// recurring event (p.Instance is that occurrence's original start) so it
// applies to the series: a moved start shifts the series by the same amount,
// and a new end keeps the edited length rather than being read as an
// absolute end for the series' first occurrence.
func anchorToSeries(p *EventPatch, m *component, rule string, loc *time.Location) error {
	var instance string
	if rule == "" || len(p.Instance) == 0 || json.Unmarshal(p.Instance, &instance) != nil || instance == "" {
		return nil
	}
	if p.Start == nil && p.End == nil {
		return nil
	}
	// Inputs are read in our zone (that's what the owner means by "10:00"), but
	// the series repeats in its own DTSTART zone (another client may have
	// written it in UTC), so occurrences and wall-clock shifts use that one.
	zone := loc
	if m.loc != nil {
		zone = m.loc
	}
	occ, _, err := parseInput(instance, loc)
	if err != nil {
		return badInput("instance: %v", err)
	}
	// The edit was made looking at that occurrence. If the series has moved
	// since (another edit, or our own first attempt before an ETag retry), the
	// occurrence no longer exists and applying the shift again would compound.
	if !isOccurrence(m.start, occ, rule, zone) {
		return errConflict
	}
	// shift moves t by the distance from `from` to `to` on the wall clock: whole
	// calendar days plus the change in time of day, in the series' zone. Elapsed
	// time would pick up a DST hour when the occurrence and the series' first
	// date sit on different sides of a change.
	shift := func(t, from, to time.Time, dateOnly bool) string {
		days := civilDays(to, zone) - civilDays(from, zone)
		if dateOnly {
			return t.In(zone).AddDate(0, 0, days).Format(dateLayout)
		}
		x, f, tt := t.In(zone), from.In(zone), to.In(zone)
		secs := clockSeconds(tt) - clockSeconds(f)
		return time.Date(x.Year(), x.Month(), x.Day()+days, x.Hour(), x.Minute(), x.Second()+secs, x.Nanosecond(), zone).Format(time.RFC3339)
	}
	newStart, base := m.start, occ
	if p.Start != nil {
		ns, dateOnly, err := parseInput(*p.Start, loc)
		if err != nil {
			return badInput("start: %v", err)
		}
		v := shift(m.start, occ, ns, dateOnly)
		if newStart, _, err = parseInput(v, loc); err != nil {
			return badInput("start: %v", err)
		}
		base, p.Start = ns, &v
	}
	if p.End != nil {
		ne, dateOnly, err := parseInput(*p.End, loc)
		if err != nil {
			return badInput("end: %v", err)
		}
		v := shift(newStart, base, ne, dateOnly)
		p.End = &v
	}
	return nil
}

// clockSeconds is the time of day of t (already in the right zone) in seconds.
func clockSeconds(t time.Time) int { return t.Hour()*3600 + t.Minute()*60 + t.Second() }

// civilDays counts calendar days since the epoch for t's date in loc.
func civilDays(t time.Time, loc *time.Location) int {
	y, mo, d := t.In(loc).Date()
	return int(time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

func mergePatch(cur Event, m *component, p EventPatch, loc *time.Location) (EventInput, error) {
	in := EventInput{
		Title:    cur.Title,
		Start:    cur.Start,
		End:      cur.End,
		Location: cur.Location,
		Notes:    cur.Notes,
	}
	if cur.RRule != nil {
		in.RRule = *cur.RRule
	}
	allDay := cur.AllDay
	in.AllDay = &allDay
	if p.AllDay != nil {
		in.AllDay = p.AllDay
	}
	if p.Title != nil {
		in.Title = *p.Title
	}
	if p.Location != nil {
		in.Location = *p.Location
	}
	if p.Notes != nil {
		in.Notes = *p.Notes
	}
	if set, v, err := p.rrulePatch(); err != nil {
		return in, err
	} else if set {
		in.RRule = v
	}
	if p.Start != nil {
		in.Start = *p.Start
		if p.End == nil {
			// Keep the length: end = new start + old length.
			ns, dateOnly, err := parseInput(*p.Start, loc)
			if err != nil {
				return in, fmt.Errorf("start: %w", err)
			}
			switch {
			case m.allDay && *in.AllDay:
				in.End = dayStart(ns, loc).AddDate(0, 0, daysBetween(m.start, m.end)).Format(dateLayout)
			case !m.allDay && !*in.AllDay && !dateOnly:
				in.End = ns.Add(m.end.Sub(m.start)).Format(time.RFC3339)
			default:
				in.End = ""
			}
		}
	}
	if p.End != nil {
		in.End = *p.End
	}
	return in, nil
}

// checkMoveAgainstRule refuses moving a series' start somewhere its rule
// doesn't produce an occurrence: with BYDAY=MO the series keeps producing
// Mondays however the start moves, so "every occurrence" would silently not
// move. Membership is asked of the rule itself (anchored at the new start), so
// negative BYMONTHDAY, BYSETPOS and the rest are handled exactly.
func checkMoveAgainstRule(from, to time.Time, rule string, loc *time.Location) error {
	if rule == "" || from.Equal(to) {
		return nil
	}
	opt, err := rrule.StrToROptionInLocation(rule, loc)
	if err != nil {
		return nil // validated elsewhere
	}
	opt.Dtstart = to.In(loc)
	opt.Count, opt.Until = 0, time.Time{} // membership only; limits don't change which instants match
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil
	}
	first, ok := firstAt(r, to)
	if ok && !first.Equal(to) {
		return badInput("this event repeats by its rule (%s), and the new start isn't one of its occurrences, so the other occurrences wouldn't move; change the repeat rule too", rule)
	}
	return nil
}

// firstAt is the rule's first occurrence at or after t. ok is false if rrule-go
// panicked (it accepts some rules it can't evaluate); callers then let the
// edit through rather than block it on our check.
func firstAt(r *rrule.RRule, t time.Time) (first time.Time, ok bool) {
	defer func() {
		if recover() != nil {
			first, ok = time.Time{}, false
		}
	}()
	return r.After(t.Add(-time.Second), true), true
}

// isOccurrence reports whether t is an occurrence of a series starting at start
// with rule. A rule rrule-go can't evaluate counts as matching, so the check
// never blocks an edit on our own limitation.
func isOccurrence(start, t time.Time, rule string, loc *time.Location) bool {
	if t.Equal(start) {
		return true
	}
	opt, err := rrule.StrToROptionInLocation(rule, loc)
	if err != nil {
		return true
	}
	opt.Dtstart = start.In(loc)
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return true
	}
	first, ok := firstAt(r, t)
	return !ok || first.Equal(t)
}

// etagValue is an ETag without quotes or a weak marker, for use as a version.
func etagValue(etag string) string {
	return strings.Trim(strings.TrimPrefix(etag, "W/"), `"`)
}
