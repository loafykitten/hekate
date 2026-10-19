// Package when parses the dates and times people and models write, and
// computes repeating-rule occurrences, always in the configured time zone.
package when

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DateLayout is the all-day form.
const DateLayout = "2006-01-02"

// Value is a parsed date or time. A date-only value covers the whole local day.
type Value struct {
	T        time.Time // midnight local for a date-only value
	DateOnly bool
}

// local layouts are read in the configured zone; the zone-aware ones carry their own.
var localLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// Parse reads "2026-10-20", "2026-10-20T09:00" (or with seconds, or a space
// instead of the T) in loc, or a full RFC 3339 time with its own offset.
func Parse(s string, loc *time.Location) (Value, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Value{}, errors.New("empty date or time")
	}
	if loc == nil {
		loc = time.Local
	}
	if t, err := time.ParseInLocation(DateLayout, s, loc); err == nil {
		return Value{T: t, DateOnly: true}, nil
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return Value{T: t}, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return Value{T: t.In(loc)}, nil
	}
	return Value{}, fmt.Errorf("cannot read %q as a date or time; use 2026-10-20, 2026-10-20T09:00 (local time) or RFC 3339", s)
}

// String renders the value the way the API does: YYYY-MM-DD for a date, else
// RFC 3339 with the zone's offset.
func (v Value) String() string {
	if v.DateOnly {
		return v.T.Format(DateLayout)
	}
	return v.T.Format(time.RFC3339)
}

// Format renders a stored UTC instant in loc as RFC 3339.
func Format(t time.Time, loc *time.Location) string { return t.In(loc).Format(time.RFC3339) }

// StartOfDay returns local midnight of t's day in loc.
func StartOfDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
