package calendar

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustSeries(t *testing.T, ics string, loc *time.Location) *series {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(ics, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	s, err := parseSeries(cal, loc)
	if err != nil {
		t.Fatalf("parseSeries: %v", err)
	}
	return s
}

func at(t *testing.T, loc *time.Location, v string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02T15:04", v, loc)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func starts(occ []occurrence) []string {
	out := make([]string, len(occ))
	for i, o := range occ {
		out[i] = o.Start
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A weekly Tuesday 15:00 Chicago event that crosses the end of daylight
// saving time (2026-11-01), with one cancelled instance (EXDATE) and one
// instance moved and renamed (RECURRENCE-ID override).
const weekly = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:weekly-1
DTSTAMP:20261001T000000Z
DTSTART;TZID=America/Chicago:20261020T150000
DTEND;TZID=America/Chicago:20261020T160000
RRULE:FREQ=WEEKLY;BYDAY=TU
EXDATE;TZID=America/Chicago:20261027T150000
SUMMARY:Standup\, weekly
LOCATION:Room 4
END:VEVENT
BEGIN:VEVENT
UID:weekly-1
DTSTAMP:20261001T000000Z
RECURRENCE-ID;TZID=America/Chicago:20261110T150000
DTSTART;TZID=America/Chicago:20261110T163000
DTEND;TZID=America/Chicago:20261110T173000
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR
`

func TestWeeklyWithExdateOverrideAndDST(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, weekly, loc)
	occ, err := s.occurrences(at(t, loc, "2026-10-19T00:00"), at(t, loc, "2026-11-24T00:00"), loc)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"2026-10-20T15:00:00-05:00",
		// 10-27 is excluded
		"2026-11-03T15:00:00-06:00", // wall clock stays 15:00 across the DST change
		"2026-11-10T16:30:00-06:00", // the moved instance
		"2026-11-17T15:00:00-06:00",
	}
	if got := starts(occ); !equal(got, want) {
		t.Fatalf("starts = %v\nwant     %v", got, want)
	}
	if occ[0].Title != "Standup, weekly" || occ[0].Location != "Room 4" {
		t.Errorf("first = %+v", occ[0].Event)
	}
	if occ[0].End != "2026-10-20T16:00:00-05:00" || occ[1].End != "2026-11-03T16:00:00-06:00" {
		t.Errorf("ends = %s, %s", occ[0].End, occ[1].End)
	}
	if occ[2].Title != "Standup (moved)" || occ[2].End != "2026-11-10T17:30:00-06:00" {
		t.Errorf("override = %+v", occ[2].Event)
	}
	if occ[2].Instance == nil || *occ[2].Instance != "2026-11-10T15:00:00-06:00" {
		t.Errorf("override instance = %v", occ[2].Instance)
	}
	if occ[0].Instance == nil || *occ[0].Instance != occ[0].Start {
		t.Errorf("instance = %v", occ[0].Instance)
	}
	if occ[0].RRule == nil || *occ[0].RRule != "FREQ=WEEKLY;BYDAY=TU" {
		t.Errorf("rrule = %v", occ[0].RRule)
	}
	if occ[0].UID != "weekly-1" || occ[0].CreatedByHekate {
		t.Errorf("uid/created = %q %v", occ[0].UID, occ[0].CreatedByHekate)
	}
}

func TestOverrideMovedIntoWindowAndOutOfIt(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, weekly, loc)
	// Window holds only the moved instance's new time, not its original slot.
	occ, err := s.occurrences(at(t, loc, "2026-11-10T16:00"), at(t, loc, "2026-11-10T18:00"), loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := starts(occ); !equal(got, []string{"2026-11-10T16:30:00-06:00"}) {
		t.Fatalf("got %v", got)
	}
	// The original slot's window shows nothing: the instance moved away.
	occ, err = s.occurrences(at(t, loc, "2026-11-10T14:00"), at(t, loc, "2026-11-10T16:00"), loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(occ) != 0 {
		t.Fatalf("got %v", starts(occ))
	}
}

func TestDSTSpringForward(t *testing.T) {
	loc := chicago(t)
	// Daily 08:30 across 2027-03-14 (clocks go forward at 02:00).
	s := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:daily
DTSTAMP:20270101T000000Z
DTSTART;TZID=America/Chicago:20270312T083000
DTEND;TZID=America/Chicago:20270312T093000
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Run
END:VEVENT
END:VCALENDAR
`, loc)
	occ, err := s.occurrences(at(t, loc, "2027-03-01T00:00"), at(t, loc, "2027-04-01T00:00"), loc)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"2027-03-12T08:30:00-06:00", "2027-03-13T08:30:00-06:00",
		"2027-03-14T08:30:00-05:00", "2027-03-15T08:30:00-05:00",
	}
	if got := starts(occ); !equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestAllDayEvents(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:single
DTSTAMP:20261001T000000Z
DTSTART;VALUE=DATE:20261020
SUMMARY:Holiday
END:VEVENT
END:VCALENDAR
`, loc)
	occ, err := s.occurrences(at(t, loc, "2026-10-20T00:00"), at(t, loc, "2026-10-21T00:00"), loc)
	if err != nil || len(occ) != 1 {
		t.Fatalf("occ=%v err=%v", occ, err)
	}
	e := occ[0]
	if !e.AllDay || e.Start != "2026-10-20" || e.End != "2026-10-21" || e.Instance != nil {
		t.Errorf("event = %+v", e.Event)
	}
	// The day after is not covered: end is exclusive.
	occ, _ = s.occurrences(at(t, loc, "2026-10-21T00:00"), at(t, loc, "2026-10-22T00:00"), loc)
	if len(occ) != 0 {
		t.Errorf("next day shows %v", starts(occ))
	}

	multi := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:trip
DTSTAMP:20261001T000000Z
DTSTART;VALUE=DATE:20261030
DTEND;VALUE=DATE:20261102
SUMMARY:Trip
END:VEVENT
END:VCALENDAR
`, loc)
	occ, _ = multi.occurrences(at(t, loc, "2026-11-01T10:00"), at(t, loc, "2026-11-01T11:00"), loc)
	if len(occ) != 1 || occ[0].Start != "2026-10-30" || occ[0].End != "2026-11-02" {
		t.Errorf("multi-day = %v", occ)
	}

	weeklyAll := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:bin
DTSTAMP:20261001T000000Z
DTSTART;VALUE=DATE:20261026
DURATION:P1D
RRULE:FREQ=WEEKLY;UNTIL=20261109
EXDATE;VALUE=DATE:20261102
SUMMARY:Bins
END:VEVENT
END:VCALENDAR
`, loc)
	occ, _ = weeklyAll.occurrences(at(t, loc, "2026-10-01T00:00"), at(t, loc, "2026-12-01T00:00"), loc)
	if got := starts(occ); !equal(got, []string{"2026-10-26", "2026-11-09"}) {
		t.Errorf("weekly all-day = %v", got)
	}
}

func TestUTCAndFloatingTimes(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:utc
DTSTAMP:20261001T000000Z
DTSTART:20261020T200000Z
DTEND:20261020T210000Z
SUMMARY:UTC
END:VEVENT
BEGIN:VEVENT
UID:utc
DTSTAMP:20261001T000000Z
RECURRENCE-ID:20261021T200000Z
DTSTART:20261021T200000Z
DURATION:PT30M
SUMMARY:ignored override without rule
END:VEVENT
END:VCALENDAR
`, loc)
	occ, err := s.occurrences(at(t, loc, "2026-10-20T00:00"), at(t, loc, "2026-10-23T00:00"), loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := starts(occ); !equal(got, []string{"2026-10-20T15:00:00-05:00", "2026-10-21T15:00:00-05:00"}) {
		t.Errorf("utc = %v", got)
	}
	if occ[1].End != "2026-10-21T15:30:00-05:00" {
		t.Errorf("duration end = %s", occ[1].End)
	}

	f := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:floating
DTSTAMP:20261001T000000Z
DTSTART:20261020T090000
DTEND:20261020T100000
SUMMARY:Floating
END:VEVENT
END:VCALENDAR
`, loc)
	occ, _ = f.occurrences(at(t, loc, "2026-10-20T00:00"), at(t, loc, "2026-10-21T00:00"), loc)
	if got := starts(occ); !equal(got, []string{"2026-10-20T09:00:00-05:00"}) {
		t.Errorf("floating = %v", got)
	}
}

func TestUnknownTZIDFallsBackToConfiguredZone(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VTIMEZONE
TZID:Central Standard Time
X-LIC-LOCATION:America/Chicago
END:VTIMEZONE
BEGIN:VEVENT
UID:win
DTSTAMP:20261001T000000Z
DTSTART;TZID=Central Standard Time:20261020T090000
DTEND;TZID=Central Standard Time:20261020T100000
SUMMARY:Windows client
END:VEVENT
BEGIN:VEVENT
UID:win
DTSTAMP:20261001T000000Z
DTSTART;TZID=Nonexistent/Zone:20261021T090000
SUMMARY:Unknown zone
END:VEVENT
END:VCALENDAR
`, loc)
	occ, err := s.occurrences(at(t, loc, "2026-10-19T00:00"), at(t, loc, "2026-10-23T00:00"), loc)
	if err != nil || len(occ) != 1 || occ[0].Start != "2026-10-20T09:00:00-05:00" {
		t.Fatalf("occ=%v err=%v", starts(occ), err)
	}
}

func TestCancelledAndRepeatingTooOften(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:c
DTSTAMP:20261001T000000Z
DTSTART:20261020T090000Z
STATUS:CANCELLED
SUMMARY:Cancelled
END:VEVENT
END:VCALENDAR
`, loc)
	occ, err := s.occurrences(at(t, loc, "2026-10-19T00:00"), at(t, loc, "2026-10-23T00:00"), loc)
	if err != nil || len(occ) != 0 {
		t.Fatalf("occ=%v err=%v", occ, err)
	}
	if _, err := validateRRule("FREQ=HOURLY"); err == nil {
		t.Error("hourly rule accepted")
	}
	if v, err := validateRRule("RRULE:FREQ=WEEKLY;BYDAY=MO"); err != nil || v != "FREQ=WEEKLY;BYDAY=MO" {
		t.Errorf("v=%q err=%v", v, err)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	loc := chicago(t)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	sp, err := resolveSpec(EventInput{
		Title: "Dentist, Dr. Fox", Start: "2026-10-20T15:00", Location: "Main St; Suite 2",
		Notes: "Bring forms\nand card", RRule: "FREQ=MONTHLY;COUNT=3",
	}, loc)
	if err != nil {
		t.Fatal(err)
	}
	cal := newCalendarObject("uid-1", sp, loc, now)
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{
		"UID:uid-1", "DTSTART;TZID=America/Chicago:20261020T150000", "DTEND;TZID=America/Chicago:20261020T160000",
		"X-HEKATE-CREATED:TRUE", "RRULE:FREQ=MONTHLY;COUNT=3", "DTSTAMP:20261019T120000Z", "SUMMARY:Dentist\\, Dr. Fox",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	back, err := ical.NewDecoder(strings.NewReader(text)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	ser, err := parseSeries(back, loc)
	if err != nil {
		t.Fatal(err)
	}
	if !ser.createdByHekate() {
		t.Error("created_by_hekate lost")
	}
	occ, _ := ser.occurrences(at(t, loc, "2026-10-01T00:00"), at(t, loc, "2027-03-01T00:00"), loc)
	if got := starts(occ); !equal(got, []string{"2026-10-20T15:00:00-05:00", "2026-11-20T15:00:00-06:00", "2026-12-20T15:00:00-06:00"}) {
		t.Errorf("monthly = %v", got)
	}
	if occ[0].Title != "Dentist, Dr. Fox" || occ[0].Location != "Main St; Suite 2" || occ[0].Notes != "Bring forms\nand card" {
		t.Errorf("text = %+v", occ[0].Event)
	}
}

func TestSerializeAllDayAndUTCZone(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	sp, err := resolveSpec(EventInput{Title: "Trip", Start: "2026-10-30", End: "2026-11-02"}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(newCalendarObject("u", sp, time.UTC, now)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DTSTART;VALUE=DATE:20261030", "DTEND;VALUE=DATE:20261102"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in\n%s", want, buf.String())
		}
	}
	sp, _ = resolveSpec(EventInput{Title: "x", Start: "2026-10-20T15:00"}, time.UTC)
	buf.Reset()
	_ = ical.NewEncoder(&buf).Encode(newCalendarObject("u", sp, time.UTC, now))
	if !strings.Contains(buf.String(), "DTSTART:20261020T150000Z") {
		t.Errorf("UTC zone should write Z times:\n%s", buf.String())
	}
}

func TestResolveSpec(t *testing.T) {
	loc := chicago(t)
	yes, no := true, false
	tests := []struct {
		name     string
		in       EventInput
		start    string
		end      string
		allDay   bool
		errMatch string
	}{
		{"timed default hour", EventInput{Title: "a", Start: "2026-10-20T15:00"}, "2026-10-20T15:00:00-05:00", "2026-10-20T16:00:00-05:00", false, ""},
		{"rfc3339 utc", EventInput{Title: "a", Start: "2026-10-20T20:00:00Z", End: "2026-10-20T21:30:00Z"}, "2026-10-20T15:00:00-05:00", "2026-10-20T16:30:00-05:00", false, ""},
		{"date means all day", EventInput{Title: "a", Start: "2026-10-20"}, "2026-10-20", "2026-10-21", true, ""},
		{"all day flag with time", EventInput{Title: "a", Start: "2026-10-20T15:00", AllDay: &yes}, "2026-10-20", "2026-10-21", true, ""},
		{"all day end equal start is one day", EventInput{Title: "a", Start: "2026-10-20", End: "2026-10-20"}, "2026-10-20", "2026-10-21", true, ""},
		{"all day multi", EventInput{Title: "a", Start: "2026-10-20", End: "2026-10-23"}, "2026-10-20", "2026-10-23", true, ""},
		{"date but not all day", EventInput{Title: "a", Start: "2026-10-20", AllDay: &no}, "", "", false, "time of day"},
		{"no title", EventInput{Start: "2026-10-20"}, "", "", false, "title"},
		{"end before start", EventInput{Title: "a", Start: "2026-10-20T15:00", End: "2026-10-20T14:00"}, "", "", false, "after start"},
		{"bad start", EventInput{Title: "a", Start: "tomorrow"}, "", "", false, "cannot read"},
		{"bad rrule", EventInput{Title: "a", Start: "2026-10-20", RRule: "FREQ=NOPE"}, "", "", false, "rrule"},
		{"timed end date only", EventInput{Title: "a", Start: "2026-10-20T15:00", End: "2026-10-21"}, "", "", false, "time of day"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp, err := resolveSpec(tt.in, loc)
			if tt.errMatch != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errMatch) {
					t.Fatalf("err = %v, want containing %q", err, tt.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := formatTime(sp.start, sp.allDay, loc); got != tt.start {
				t.Errorf("start = %s, want %s", got, tt.start)
			}
			if got := formatTime(sp.end, sp.allDay, loc); got != tt.end {
				t.Errorf("end = %s, want %s", got, tt.end)
			}
			if sp.allDay != tt.allDay {
				t.Errorf("allDay = %v", sp.allDay)
			}
		})
	}
}

func TestMergePatchKeepsLengthWhenMoving(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, weekly, loc)
	cur := s.event(*s.master, loc, nil)
	newStart := "2026-10-21T10:00"
	in, err := mergePatch(cur, s.master, EventPatch{Start: &newStart}, loc)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := resolveSpec(in, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := formatTime(sp.end, false, loc); got != "2026-10-21T11:00:00-05:00" {
		t.Errorf("end = %s", got)
	}
	if sp.rrule != "FREQ=WEEKLY;BYDAY=TU" || sp.title != "Standup, weekly" {
		t.Errorf("spec = %+v", sp)
	}
}

func TestApplyUpdateReschedulingDropsExceptions(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, weekly, loc)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	sp := spec{title: "Renamed", start: at(t, loc, "2026-10-20T15:00"), end: at(t, loc, "2026-10-20T16:00"), rrule: "FREQ=WEEKLY;BYDAY=TU"}

	if err := s.applyUpdate(sp, false, loc, now); err != nil {
		t.Fatal(err)
	}
	if s.master.c.Props.Get("EXDATE") == nil || len(s.cal.Children) != 2 {
		t.Error("plain edit dropped exceptions")
	}
	if got := rawText(s.master.c.Props.Get("SEQUENCE")); got != "1" {
		t.Errorf("sequence = %q", got)
	}

	if err := s.applyUpdate(sp, true, loc, now); err != nil {
		t.Fatal(err)
	}
	if s.master.c.Props.Get("EXDATE") != nil || len(s.cal.Children) != 1 {
		t.Error("reschedule kept exceptions")
	}
}

// Edits made while viewing a later occurrence apply to the series in relative
// terms (the review case: extending the Oct 26 occurrence to 11:00 must make
// the series two hours long, not run from Oct 19 to Oct 26).
func TestAnchorToSeries(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02T15:04", s, loc); return v }
	master := &component{start: at("2026-10-19T09:00"), end: at("2026-10-19T10:00")}
	str := func(s string) *string { return &s }
	inst := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }

	tests := []struct {
		name               string
		p                  EventPatch
		wantStart, wantEnd string
	}{
		{"end only, later occurrence", EventPatch{Instance: inst("2026-10-26T09:00:00-05:00"), End: str("2026-10-26T11:00")},
			"", "2026-10-19T11:00:00-05:00"},
		{"moved later by an hour", EventPatch{Instance: inst("2026-10-26T09:00:00-05:00"), Start: str("2026-10-26T10:00"), End: str("2026-10-26T11:00")},
			"2026-10-19T10:00:00-05:00", "2026-10-19T11:00:00-05:00"},
		{"occurrence after the November change", EventPatch{Instance: inst("2026-11-02T09:00:00-06:00"), End: str("2026-11-02T11:30")},
			"", "2026-10-19T11:30:00-05:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.p
			if err := anchorToSeries(&p, master, true, loc); err != nil {
				t.Fatal(err)
			}
			if got := deref(p.Start); got != tt.wantStart {
				t.Errorf("start = %q, want %q", got, tt.wantStart)
			}
			if got := deref(p.End); got != tt.wantEnd {
				t.Errorf("end = %q, want %q", got, tt.wantEnd)
			}
		})
	}

	// A non-recurring event, or no instance, is left alone.
	p := EventPatch{End: str("2026-10-26T11:00")}
	if err := anchorToSeries(&p, master, true, loc); err != nil || *p.End != "2026-10-26T11:00" {
		t.Errorf("without instance: %v %q", err, *p.End)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// All-day series move by calendar days, never picking up a DST hour (the review
// case: moving the Nov 2 occurrence to Oct 26 must land the series on Oct 12).
func TestAnchorToSeriesAllDayAcrossDST(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	day := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02", s, loc); return v }
	master := &component{start: day("2026-10-19"), end: day("2026-10-20"), allDay: true}
	str := func(s string) *string { return &s }
	inst, _ := json.Marshal("2026-11-02")
	p := EventPatch{Instance: inst, Start: str("2026-10-26"), End: str("2026-10-28")}
	if err := anchorToSeries(&p, master, true, loc); err != nil {
		t.Fatal(err)
	}
	if *p.Start != "2026-10-12" || *p.End != "2026-10-14" {
		t.Errorf("start, end = %s, %s; want 2026-10-12, 2026-10-14", *p.Start, *p.End)
	}
}

// Update retries its edit after an ETag conflict; each attempt must translate
// the caller's original patch, not the previous attempt's result.
func TestAnchorToSeriesIsRepeatableOnACopy(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02T15:04", s, loc); return v }
	master := &component{start: at("2026-10-19T09:00"), end: at("2026-10-19T10:00")}
	str := func(s string) *string { return &s }
	inst, _ := json.Marshal("2026-10-26T09:00:00-05:00")
	p := EventPatch{Instance: inst, Start: str("2026-10-26T10:00"), End: str("2026-10-26T11:00")}
	var starts []string
	for range 2 { // first attempt, then the retry
		attempt := p
		if err := anchorToSeries(&attempt, master, true, loc); err != nil {
			t.Fatal(err)
		}
		starts = append(starts, *attempt.Start)
	}
	if starts[0] != "2026-10-19T10:00:00-05:00" || starts[1] != starts[0] || *p.Start != "2026-10-26T10:00" {
		t.Errorf("attempts = %v, caller's start = %s", starts, *p.Start)
	}
}

// Rules that would pack thousands of times into each day are refused, whether
// they come from Renamon or from another calendar client.
func TestDenseRulesRejected(t *testing.T) {
	hours := "0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23"
	for _, rule := range []string{
		"FREQ=HOURLY",
		"FREQ=DAILY;BYHOUR=" + hours + ";BYMINUTE=0,30",
		"FREQ=WEEKLY;BYHOUR=9;BYMINUTE=" + hours + ";BYSECOND=0,1",
	} {
		if _, err := validateRRule(rule); err == nil {
			t.Errorf("%s accepted", rule)
		}
	}
	for _, rule := range []string{"FREQ=DAILY", "FREQ=WEEKLY;BYDAY=MO,WE", "FREQ=DAILY;BYHOUR=" + hours} {
		if _, err := validateRRule(rule); err != nil {
			t.Errorf("%s rejected: %v", rule, err)
		}
	}
}

// Renaming another client's recurring event must not rewrite its schedule in
// our zone: anchored at 14:00 UTC, it stays 14:00 UTC through the DST change.
func TestUnrelatedEditKeepsScheduleAsWritten(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	ser := mustSeries(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//other client//EN
BEGIN:VEVENT
UID:standup
DTSTAMP:20261001T000000Z
DTSTART:20261019T140000Z
DTEND:20261019T150000Z
RRULE:FREQ=WEEKLY
SUMMARY:Standup
END:VEVENT
END:VCALENDAR
`, loc)
	title := "Team standup"
	cur := ser.event(*ser.master, loc, nil)
	in, err := mergePatch(cur, ser.master, EventPatch{Title: &title}, loc)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := resolveSpec(in, loc)
	if err != nil {
		t.Fatal(err)
	}
	resched := !sp.start.Equal(ser.master.start) || sp.allDay != ser.master.allDay || sp.rrule != ser.rruleString()
	if err := ser.applyUpdate(sp, resched, loc, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := ser.master.c
	if got := m.Props.Get(ical.PropDateTimeStart); got.Value != "20261019T140000Z" || got.Params.Get(ical.ParamTimezoneID) != "" {
		t.Errorf("DTSTART = %q %v, want unchanged 20261019T140000Z", got.Value, got.Params)
	}
	if got := m.Props.Get(ical.PropSummary).Value; got != title {
		t.Errorf("SUMMARY = %q", got)
	}
}
