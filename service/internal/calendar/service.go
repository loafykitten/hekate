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
	return s.mainEvent(ser), nil
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
	if _, err := s.dav.put(ctx, objectPath(col, uid), cal, ""); err != nil {
		return Event{}, err
	}
	s.changed()
	ser, err := parseSeries(cal, s.loc)
	if err != nil {
		return Event{}, err
	}
	return s.mainEvent(ser), nil
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

	UID             json.RawMessage `json:"uid"`
	Instance        json.RawMessage `json:"instance"`
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
	err := s.mutate(ctx, uid, func(o *object, ser *series) (bool, error) {
		if ser.master == nil {
			return false, badInput("this event has no main entry to edit")
		}
		if onlyHekate && !ser.createdByHekate() {
			return false, ErrNotOwned
		}
		cur := ser.event(*ser.master, s.loc, nil)
		in, err := mergePatch(cur, ser.master, p, s.loc)
		if err != nil {
			return false, err
		}
		sp, err := resolveSpec(in, s.loc)
		if err != nil {
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
func (s *Service) mutate(ctx context.Context, uid string, fn func(*object, *series) (bool, error)) error {
	if s.dav == nil {
		return ErrNotConfigured
	}
	for attempt := 0; ; attempt++ {
		o, err := s.dav.find(ctx, uid)
		if err != nil {
			return err
		}
		ser, err := parseSeries(o.cal, s.loc)
		if err != nil {
			return err
		}
		write, err := fn(o, ser)
		if err != nil {
			return err
		}
		if !write {
			return nil
		}
		_, err = s.dav.put(ctx, o.path, o.cal, o.etag)
		if errors.Is(err, errConflict) && attempt == 0 {
			continue
		}
		if err != nil {
			if errors.Is(err, errConflict) {
				return fmt.Errorf("%w; try again", errConflict)
			}
			return err
		}
		s.changed()
		return nil
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
