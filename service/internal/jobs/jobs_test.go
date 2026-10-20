package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/store"
)

func newRunner(t *testing.T, now *time.Time) *Runner {
	t.Helper()
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r, err := New(Options{Store: db, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDaily(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 20, h, m, 0, 0, loc) }
	next := Daily(loc, 6*time.Hour, func(context.Context) (Clock, bool) { return Clock{3, 30}, true })
	cases := []struct {
		name      string
		now, last time.Time
		want      time.Time
	}{
		{"ahead today", at(2, 0), at(3, 30).AddDate(0, 0, -1), at(3, 30)},
		{"due now", at(3, 30), at(3, 30).AddDate(0, 0, -1), at(3, 30)},
		{"missed, caught up within the window", at(8, 0), at(3, 30).AddDate(0, 0, -1), at(3, 30)},
		{"missed too long ago: tomorrow", at(10, 0), at(3, 30).AddDate(0, 0, -1), at(3, 30).AddDate(0, 0, 1)},
		{"already ran today", at(4, 0), at(3, 31), at(3, 30).AddDate(0, 0, 1)},
		{"never ran, past the time", at(5, 0), time.Time{}, at(3, 30)},
	}
	for _, c := range cases {
		got, ok := next(t.Context(), c.now, c.last)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s: next = %v, %v; want %v", c.name, got, ok, c.want)
		}
	}
	off := Daily(loc, time.Hour, func(context.Context) (Clock, bool) { return Clock{}, false })
	if _, ok := off(t.Context(), at(9, 0), time.Time{}); ok {
		t.Error("a switched-off daily job has a next run")
	}
	// Across the end of daylight saving time, tomorrow's 03:30 is still 03:30 local.
	nov := time.Date(2026, 10, 31, 4, 0, 0, 0, loc)
	got, _ := next(t.Context(), nov, time.Date(2026, 10, 31, 3, 30, 0, 0, loc))
	if want := time.Date(2026, 11, 1, 3, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("across DST: %v, want %v", got, want)
	}
}

func TestEvery(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	next := Every(30 * time.Minute)
	if got, _ := next(t.Context(), now, time.Time{}); !got.Equal(now) {
		t.Errorf("never ran: %v", got)
	}
	if got, _ := next(t.Context(), now, now.Add(-10*time.Minute)); !got.Equal(now.Add(20 * time.Minute)) {
		t.Errorf("ran 10 min ago: %v", got)
	}
}

func TestRunsAreRecorded(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r := newRunner(t, &now)
	r.Add(Job{Name: "good", Run: func(context.Context) (string, error) { return "wrote the diary", nil }})
	r.Add(Job{Name: "bad", Run: func(context.Context) (string, error) { return "", errors.New("model\nunreachable") }})
	r.Add(Job{Name: "idle", Run: func(context.Context) (string, error) { return "", ErrSkip }})
	for _, name := range []string{"good", "bad", "idle"} {
		if err := r.RunNow(t.Context(), name); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if err := r.RunNow(t.Context(), "nope"); err == nil {
		t.Error("running an unknown job should fail")
	}
	latest, err := r.Latest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if g := latest["good"]; g == nil || g.Status != StatusOK || g.Summary != "wrote the diary" || g.FinishedAt == nil {
		t.Errorf("good = %+v", g)
	}
	if b := latest["bad"]; b == nil || b.Status != StatusFailed || b.Error != "model unreachable" {
		t.Errorf("bad = %+v", b)
	}
	if latest["idle"] != nil {
		t.Errorf("a skipped run is not recorded: %+v", latest["idle"])
	}
}

func TestLoopRunsWhenDueAndCatchesUpInterruptedRuns(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r := newRunner(t, &now)
	// A run cut off by a restart.
	if _, err := r.begin(t.Context(), "tick", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ran := 0
	r.sleep = func(ctx context.Context, d time.Duration, _ <-chan struct{}) {
		now = now.Add(d) // time passes instantly
		if now.After(time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)) {
			cancel()
		}
	}
	r.Add(Job{Name: "tick", Next: Every(30 * time.Minute), Run: func(context.Context) (string, error) {
		ran++
		return "", nil
	}})
	r.Run(ctx)
	// The cut-off run at 11:00 counts as the last one, so it is overdue: 12:00 (now), then 12:30, 13:00, 13:30, 14:00.
	if ran != 5 {
		t.Errorf("ran %d times", ran)
	}
	runs, err := r.Runs(t.Context(), "tick", 0)
	if err != nil {
		t.Fatal(err)
	}
	if last := runs[len(runs)-1]; last.Status != StatusFailed || last.Error != "cut off by a restart" {
		t.Errorf("the interrupted run = %+v", last)
	}
}

func TestParseClock(t *testing.T) {
	c, err := ParseClock(" 07:05 ")
	if err != nil || c != (Clock{7, 5}) || c.String() != "07:05" {
		t.Errorf("%v %v", c, err)
	}
	if _, err := ParseClock("7am"); err == nil {
		t.Error("7am should not parse")
	}
}
