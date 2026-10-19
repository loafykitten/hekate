package when

import (
	"testing"
	"time"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	return loc
}

func TestParse(t *testing.T) {
	loc := chicago(t)
	tests := []struct {
		in       string
		want     string
		dateOnly bool
		wantErr  bool
	}{
		{in: "2026-10-20", want: "2026-10-20", dateOnly: true},
		{in: " 2026-10-20 ", want: "2026-10-20", dateOnly: true},
		{in: "2026-10-20T09:00", want: "2026-10-20T09:00:00-05:00"},
		{in: "2026-10-20 09:30", want: "2026-10-20T09:30:00-05:00"},
		{in: "2026-10-20T09:00:15", want: "2026-10-20T09:00:15-05:00"},
		// Local time after the November change gets the winter offset.
		{in: "2026-11-02T09:00", want: "2026-11-02T09:00:00-06:00"},
		{in: "2026-10-20T14:00:00Z", want: "2026-10-20T09:00:00-05:00"},
		{in: "2026-10-20T09:00:00+02:00", want: "2026-10-20T02:00:00-05:00"},
		{in: "", wantErr: true},
		{in: "tomorrow", wantErr: true},
		{in: "2026-13-40", wantErr: true},
		{in: "09:00", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			v, err := Parse(tt.in, loc)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %v, want error", tt.in, v)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if v.String() != tt.want || v.DateOnly != tt.dateOnly {
				t.Errorf("Parse(%q) = %s dateOnly=%v, want %s dateOnly=%v", tt.in, v, v.DateOnly, tt.want, tt.dateOnly)
			}
		})
	}
}

// The clocks go back on 2026-11-01 in America/Chicago. A 09:00 rule must stay
// at 09:00 local on both sides.
func TestRuleAcrossDST(t *testing.T) {
	loc := chicago(t)
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, loc) // Monday, CDT
	r, err := ParseRule("FREQ=WEEKLY;BYDAY=MO", start, loc)
	if err != nil {
		t.Fatal(err)
	}
	next, ok := r.Next(start)
	if !ok || next.Format(time.RFC3339) != "2026-11-02T09:00:00-06:00" {
		t.Errorf("next = %v %v", next, ok)
	}

	daily, err := ParseRule("FREQ=DAILY", time.Date(2026, 10, 31, 7, 30, 0, 0, loc), loc)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	at := time.Date(2026, 10, 31, 7, 30, 0, 0, loc)
	for range 3 {
		at, _ = daily.Next(at)
		got = append(got, at.Format(time.RFC3339))
	}
	want := []string{"2026-11-01T07:30:00-06:00", "2026-11-02T07:30:00-06:00", "2026-11-03T07:30:00-06:00"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("daily[%d] = %s, want %s", i, got[i], want[i])
		}
	}

	// Spring forward (2027-03-14): 09:00 stays 09:00.
	spring, _ := ParseRule("FREQ=DAILY", time.Date(2027, 3, 13, 9, 0, 0, 0, loc), loc)
	n, _ := spring.Next(time.Date(2027, 3, 13, 9, 0, 0, 0, loc))
	if n.Format(time.RFC3339) != "2027-03-14T09:00:00-05:00" {
		t.Errorf("spring next = %s", n.Format(time.RFC3339))
	}
}

func TestRuleEnds(t *testing.T) {
	loc := chicago(t)
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	r, err := ParseRule("FREQ=DAILY;COUNT=2", start, loc)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := r.Next(start); !ok || n.Day() != 20 {
		t.Fatalf("second occurrence = %v %v", n, ok)
	}
	if n, ok := r.Next(start.AddDate(0, 0, 1)); ok {
		t.Errorf("rule should have ended, got %v", n)
	}
}

func TestParseRuleRejects(t *testing.T) {
	loc := chicago(t)
	start := time.Now()
	for _, in := range []string{
		"", "nonsense", "FREQ=SECONDLY", "FREQ=MINUTELY;INTERVAL=5", "FREQ=DAILY\nFREQ=WEEKLY",
		"DTSTART:20261020T090000;FREQ=DAILY", "FREQ=WEEKLY;BYDAY=XX",
	} {
		if _, err := ParseRule(in, start, loc); err == nil {
			t.Errorf("ParseRule(%q) accepted", in)
		}
	}
	for _, in := range []string{"FREQ=HOURLY", "RRULE:FREQ=WEEKLY;BYDAY=MO,TH", "freq=monthly;bymonthday=1"} {
		if _, err := ParseRule(in, start, loc); err != nil {
			t.Errorf("ParseRule(%q): %v", in, err)
		}
	}
}

func TestRebaseSpendsCount(t *testing.T) {
	loc := chicago(t)
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	r, _ := ParseRule("FREQ=DAILY;COUNT=5", start, loc)
	next, _ := r.Next(start)
	if got := r.Rebase(next); got != "FREQ=DAILY;COUNT=4" {
		t.Errorf("one step: %s", got)
	}
	// Skipping three occurrences (an overdue chore) spends them too.
	far := start.AddDate(0, 0, 3)
	if got := r.Rebase(far); got != "FREQ=DAILY;COUNT=2" {
		t.Errorf("three steps: %s", got)
	}
	noCount, _ := ParseRule("FREQ=DAILY;INTERVAL=2", start, loc)
	if got := noCount.Rebase(far); got != "FREQ=DAILY;INTERVAL=2" {
		t.Errorf("no count: %s", got)
	}
}

// Hourly rules narrowed by hour/minute/second can make rrule-go search
// forever, so they are refused before anything evaluates them.
func TestParseRuleRefusesHourlyTimeFilters(t *testing.T) {
	loc := time.UTC
	start := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
	for _, rule := range []string{"FREQ=HOURLY;INTERVAL=2;BYHOUR=1", "FREQ=HOURLY;BYMINUTE=30", "FREQ=HOURLY;BYSECOND=5"} {
		if _, err := ParseRule(rule, start, loc); err == nil {
			t.Errorf("%s accepted", rule)
		}
	}
	for _, rule := range []string{"FREQ=HOURLY;INTERVAL=2", "FREQ=HOURLY;INTERVAL=3;BYDAY=MO,TU", "FREQ=DAILY;BYHOUR=9,21"} {
		r, err := ParseRule(rule, start, loc)
		if err != nil {
			t.Errorf("%s rejected: %v", rule, err)
			continue
		}
		if _, ok := r.First(start); !ok {
			t.Errorf("%s has no first occurrence", rule)
		}
	}
}
