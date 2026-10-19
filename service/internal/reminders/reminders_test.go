package reminders

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

type recEvents struct {
	mu      sync.Mutex
	invalid int
	notes   []proto.Notify
}

func (r *recEvents) Invalidate(...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalid++
}

func (r *recEvents) Notify(n proto.Notify) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
}

func (r *recEvents) notices() []proto.Notify {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.notes)
}

func (r *recEvents) invalidations() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.invalid
}

type fakePush struct {
	mu   sync.Mutex
	sent []push.Payload
	urg  []push.Urgency
}

func (f *fakePush) Send(_ context.Context, p push.Payload, u push.Urgency) (push.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, p)
	f.urg = append(f.urg, u)
	return push.Result{Sent: 1}, nil
}

func (f *fakePush) payloads() []push.Payload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// clock is a hand-wound clock: After channels fire when advance passes them.
type clock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	last    time.Duration // the most recent wait asked for
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{c.now.Add(d), ch})
	c.last = d
	return ch
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(t) {
			w.ch <- t
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

func (c *clock) advance(d time.Duration) { c.set(c.Now().Add(d)) }

// lastWait is the length of the most recent sleep the scheduler asked for.
func (c *clock) lastWait() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

type fixture struct {
	svc    *Service
	events *recEvents
	push   *fakePush
	clock  *clock
	loc    *time.Location
}

// newFixture starts on Monday 2026-10-19 14:00 in America/Chicago.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{events: &recEvents{}, push: &fakePush{}, clock: &clock{now: time.Date(2026, 10, 19, 14, 0, 0, 0, loc)}, loc: loc}
	f.svc, err = New(Options{Store: db, Location: loc, Events: f.events, Push: f.push, Now: f.clock.Now, After: f.clock.After})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02T15:04", s, f.loc)
	if err != nil {
		panic(err)
	}
	return t
}

func (f *fixture) create(t *testing.T, text, at, rule string) Reminder {
	t.Helper()
	r, err := f.svc.Create(t.Context(), text, at, rule)
	if err != nil {
		t.Fatalf("Create(%q, %q): %v", text, at, err)
	}
	return r
}

// forceAt moves a reminder to an arbitrary past time, as if it had been
// scheduled long ago (Create refuses the past).
func (f *fixture) forceAt(t *testing.T, id string, at time.Time) {
	t.Helper()
	if _, err := f.svc.db.ExecContext(t.Context(), `UPDATE reminders SET at = ?, start = ? WHERE id = ?`, stamp(at), stamp(at), id); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) get(t *testing.T, id string) Reminder {
	t.Helper()
	var n int64
	for _, c := range id {
		n = n*10 + int64(c-'0')
	}
	r, err := f.svc.Get(t.Context(), n)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status {
		t.Fatalf("err = %v, want status %d", err, status)
	}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "  Call the dentist ", "2026-10-20T09:00", "")
	if r.Text != "Call the dentist" || r.At != "2026-10-20T09:00:00-05:00" || r.Status != StatusScheduled || r.RRule != nil || r.LastFiredAt != nil {
		t.Errorf("reminder = %+v", r)
	}
	in, err := f.svc.CreateIn(t.Context(), "Stretch", 2, "")
	if err != nil || in.At != "2026-10-19T14:02:00-05:00" {
		t.Errorf("in 2 minutes: %v %+v", err, in)
	}

	for name, fn := range map[string]func() error{
		"past":        func() error { _, err := f.svc.Create(t.Context(), "x", "2026-10-19T13:59", ""); return err },
		"right now":   func() error { _, err := f.svc.Create(t.Context(), "x", "2026-10-19T14:00", ""); return err },
		"date only":   func() error { _, err := f.svc.Create(t.Context(), "x", "2026-10-20", ""); return err },
		"garbage":     func() error { _, err := f.svc.Create(t.Context(), "x", "later", ""); return err },
		"no text":     func() error { _, err := f.svc.Create(t.Context(), " ", "2026-10-20T09:00", ""); return err },
		"bad rule":    func() error { _, err := f.svc.Create(t.Context(), "x", "2026-10-20T09:00", "sometimes"); return err },
		"zero in":     func() error { _, err := f.svc.CreateIn(t.Context(), "x", 0, ""); return err },
		"negative in": func() error { _, err := f.svc.CreateIn(t.Context(), "x", -5, ""); return err },
	} {
		wantStatus(t, fn(), http.StatusBadRequest)
		_ = name
	}
}

func TestFireOneOff(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "Stretch", "2026-10-19T14:02", "")
	f.create(t, "Later", "2026-10-19T18:00", "")

	if n, err := f.svc.fireDue(t.Context(), 0); err != nil || n != 0 {
		t.Fatalf("fired early: %d %v", n, err)
	}
	f.clock.set(f.at("2026-10-19T14:02"))
	n, err := f.svc.fireDue(t.Context(), 0)
	if err != nil || n != 1 {
		t.Fatalf("fire: %d %v", n, err)
	}

	got := f.get(t, r.ID)
	if got.Status != StatusFired || str(got.LastFiredAt) != "2026-10-19T14:02:00-05:00" || got.At != r.At {
		t.Errorf("after firing = %+v", got)
	}
	notes := f.events.notices()
	if len(notes) != 1 || notes[0].Kind != "reminder" || notes[0].Body != "Stretch" || notes[0].Title != "Reminder" || notes[0].Card == nil || notes[0].Card.Kind != "reminders" {
		t.Fatalf("notify = %+v", notes)
	}
	pl := f.push.payloads()
	if len(pl) != 1 || pl[0].Body != "Stretch" || pl[0].Tag != "reminder-"+r.ID || f.push.urg[0] != push.UrgencyHigh {
		t.Errorf("push = %+v %v", pl, f.push.urg)
	}
	// Firing twice would be a bug: nothing is due now.
	if n, _ := f.svc.fireDue(t.Context(), 0); n != 0 || len(f.events.notices()) != 1 {
		t.Error("fired again")
	}
	if f.events.invalidations() < 3 { // two creates and the fire
		t.Errorf("invalidations = %d", f.events.invalidations())
	}
}

func TestRepeatingReschedules(t *testing.T) {
	tests := []struct {
		name, at, rule, firesAt string
		wantNext                string
	}{
		{"daily", "2026-10-20T09:00", "FREQ=DAILY", "2026-10-20T09:00", "2026-10-21T09:00:00-05:00"},
		{"weekly across the November change", "2026-10-26T09:00", "FREQ=WEEKLY;BYDAY=MO", "2026-10-26T09:00", "2026-11-02T09:00:00-06:00"},
		{"daily across the November change", "2026-10-31T07:30", "FREQ=DAILY", "2026-10-31T07:30", "2026-11-01T07:30:00-06:00"},
		{"late by a few minutes still lands on the grid", "2026-10-20T09:00", "FREQ=DAILY", "2026-10-20T09:07", "2026-10-21T09:00:00-05:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			r := f.create(t, "Pills", tt.at, tt.rule)
			f.clock.set(f.at(tt.firesAt))
			if n, err := f.svc.fireDue(t.Context(), 0); err != nil || n != 1 {
				t.Fatalf("fire: %d %v", n, err)
			}
			got := f.get(t, r.ID)
			if got.Status != StatusScheduled || got.At != tt.wantNext || got.LastFiredAt == nil || got.RRule == nil {
				t.Errorf("after firing = %+v, want next %s", got, tt.wantNext)
			}
			if len(f.events.notices()) != 1 {
				t.Errorf("notices = %d", len(f.events.notices()))
			}
		})
	}
}

func TestRepeatEndsWithItsRule(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "Twice", "2026-10-20T09:00", "FREQ=DAILY;COUNT=2")
	f.clock.set(f.at("2026-10-20T09:00"))
	f.svc.fireDue(t.Context(), 0) //nolint:errcheck // checked through state below
	if got := f.get(t, r.ID); got.Status != StatusScheduled || got.At != "2026-10-21T09:00:00-05:00" {
		t.Fatalf("after first = %+v", got)
	}
	f.clock.set(f.at("2026-10-21T09:00"))
	f.svc.fireDue(t.Context(), 0) //nolint:errcheck // checked through state below
	if got := f.get(t, r.ID); got.Status != StatusFired {
		t.Errorf("after second = %+v, want the series closed", got)
	}
	if len(f.events.notices()) != 2 {
		t.Errorf("notices = %d", len(f.events.notices()))
	}
}

func TestMissedOnStartup(t *testing.T) {
	f := newFixture(t)
	recent := f.create(t, "an hour late", "2026-10-19T15:00", "")
	stale := f.create(t, "yesterday", "2026-10-19T15:00", "")
	edge := f.create(t, "just inside the window", "2026-10-19T15:00", "")
	repeatRecent := f.create(t, "daily, 2h late", "2026-10-19T15:00", "FREQ=DAILY")
	repeatStale := f.create(t, "daily, long missed", "2026-10-19T15:00", "FREQ=DAILY")

	f.forceAt(t, recent.ID, f.at("2026-10-19T13:00"))
	f.forceAt(t, stale.ID, f.at("2026-10-18T09:00"))
	f.forceAt(t, edge.ID, f.at("2026-10-19T02:00")) // exactly 12 h before 14:00
	f.forceAt(t, repeatRecent.ID, f.at("2026-10-19T12:00"))
	f.forceAt(t, repeatStale.ID, f.at("2026-10-16T09:00"))

	n, err := f.svc.fireDue(t.Context(), missedWindow)
	if err != nil || n != 3 {
		t.Fatalf("fired %d (%v), want 3", n, err)
	}
	var bodies []string
	for _, nt := range f.events.notices() {
		bodies = append(bodies, nt.Body)
	}
	slices.Sort(bodies)
	if !slices.Equal(bodies, []string{"an hour late", "daily, 2h late", "just inside the window"}) {
		t.Errorf("notified = %q", bodies)
	}

	if got := f.get(t, stale.ID); got.Status != StatusFired || got.LastFiredAt != nil {
		t.Errorf("stale one-off = %+v, want closed and never fired", got)
	}
	if got := f.get(t, recent.ID); got.Status != StatusFired || got.LastFiredAt == nil {
		t.Errorf("recent one-off = %+v", got)
	}
	// Repeating ones move to the next occurrence after now either way.
	for _, id := range []string{repeatRecent.ID, repeatStale.ID} {
		if got := f.get(t, id); got.Status != StatusScheduled || got.At != "2026-10-20T09:00:00-05:00" && got.At != "2026-10-20T12:00:00-05:00" && got.At != "2026-10-20T15:00:00-05:00" {
			t.Errorf("repeating %s = %+v", id, got)
		}
	}
	if got := f.get(t, repeatStale.ID); got.LastFiredAt != nil {
		t.Errorf("long-missed repeating reminder claims to have fired: %+v", got)
	}
	if len(f.push.payloads()) != 3 {
		t.Errorf("pushes = %d, want 3", len(f.push.payloads()))
	}
}

func TestSnooze(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "Pills", "2026-10-20T09:00", "FREQ=DAILY")
	f.clock.set(f.at("2026-10-20T09:00"))
	f.svc.fireDue(t.Context(), 0) //nolint:errcheck // state checked below

	// The fired occurrence is snoozed 10 minutes; the daily rule must keep 09:00.
	sn, err := f.svc.Snooze(t.Context(), mustID(r.ID), 10)
	if err != nil || sn.Status != StatusScheduled || sn.At != "2026-10-20T09:10:00-05:00" {
		t.Fatalf("snooze: %v %+v", err, sn)
	}
	f.clock.set(f.at("2026-10-20T09:10"))
	f.svc.fireDue(t.Context(), 0) //nolint:errcheck // state checked below
	if got := f.get(t, r.ID); got.At != "2026-10-21T09:00:00-05:00" {
		t.Errorf("after the snoozed fire = %+v, want the daily 09:00 grid", got)
	}
	if len(f.events.notices()) != 2 {
		t.Errorf("notices = %d, want 2", len(f.events.notices()))
	}

	one := f.create(t, "One-off", "2026-10-21T10:00", "")
	f.clock.set(f.at("2026-10-21T10:00"))
	f.svc.fireDue(t.Context(), 0) //nolint:errcheck // state checked below
	sn, err = f.svc.Snooze(t.Context(), mustID(one.ID), 30)
	if err != nil || sn.Status != StatusScheduled || sn.At != "2026-10-21T10:30:00-05:00" {
		t.Errorf("snooze fired one-off: %v %+v", err, sn)
	}

	_, err = f.svc.Snooze(t.Context(), mustID(one.ID), 0)
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.Snooze(t.Context(), mustID(one.ID), maxSnooze+1)
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.Snooze(t.Context(), 999, 5)
	wantStatus(t, err, http.StatusNotFound)
}

func mustID(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestUpdateDoneCancel(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "Stretch", "2026-10-20T09:00", "")
	id := mustID(r.ID)

	up, err := f.svc.Update(t.Context(), id, Patch{Text: ptr("Stretch your back"), At: ptr("2026-10-20T10:30")})
	if err != nil || up.Text != "Stretch your back" || up.At != "2026-10-20T10:30:00-05:00" {
		t.Fatalf("update: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{InMinutes: ptr(15)})
	if err != nil || up.At != "2026-10-19T14:15:00-05:00" {
		t.Fatalf("update in minutes: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{RRule: ptr("FREQ=DAILY")})
	if err != nil || str(up.RRule) != "FREQ=DAILY" {
		t.Fatalf("add rule: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{RRule: ptr("")})
	if err != nil || up.RRule != nil {
		t.Fatalf("clear rule: %v %+v", err, up)
	}
	_, err = f.svc.Update(t.Context(), id, Patch{At: ptr("2026-10-19T09:00")})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.Update(t.Context(), id, Patch{At: ptr("2026-10-20T10:00"), InMinutes: ptr(5)})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.Update(t.Context(), 999, Patch{})
	wantStatus(t, err, http.StatusNotFound)

	done, err := f.svc.Done(t.Context(), id)
	if err != nil || done.Status != StatusDone {
		t.Fatalf("done: %v %+v", err, done)
	}
	f.clock.set(f.at("2026-10-19T15:00"))
	if n, _ := f.svc.fireDue(t.Context(), 0); n != 0 {
		t.Error("a done reminder fired")
	}
	// Re-arming a finished reminder with a new time works.
	re, err := f.svc.Update(t.Context(), id, Patch{At: ptr("2026-10-19T16:00")})
	if err != nil || re.Status != StatusScheduled {
		t.Fatalf("re-arm: %v %+v", err, re)
	}

	c, err := f.svc.Cancel(t.Context(), id)
	if err != nil || c.Status != StatusCancelled {
		t.Fatalf("cancel: %v %+v", err, c)
	}
	f.clock.set(f.at("2026-10-19T17:00"))
	if n, _ := f.svc.fireDue(t.Context(), 0); n != 0 || len(f.events.notices()) != 0 {
		t.Error("a cancelled reminder fired")
	}

	// Done on a repeating reminder must not skip the next occurrence.
	rep := f.create(t, "Daily", "2026-10-20T09:00", "FREQ=DAILY")
	d, err := f.svc.Done(t.Context(), mustID(rep.ID))
	if err != nil || d.Status != StatusScheduled || d.At != rep.At {
		t.Errorf("done on repeating = %+v %v", d, err)
	}
}

func ptr[T any](v T) *T { return &v }

// A reminder cancelled between the scheduler's query and its update must stay quiet.
func TestFireLosesToConcurrentCancel(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "Stretch", "2026-10-19T14:05", "")
	stale := due{id: mustID(r.ID), text: "Stretch", at: f.at("2026-10-19T14:05"), start: f.at("2026-10-19T14:05"), atStamp: stamp(f.at("2026-10-19T14:05"))}
	if _, err := f.svc.Cancel(t.Context(), mustID(r.ID)); err != nil {
		t.Fatal(err)
	}
	f.clock.set(f.at("2026-10-19T14:06"))
	fired, err := f.svc.fire(t.Context(), stale, f.clock.Now(), 0)
	if err != nil || fired || len(f.events.notices()) != 0 || len(f.push.payloads()) != 0 {
		t.Errorf("fired=%v err=%v notices=%d pushes=%d", fired, err, len(f.events.notices()), len(f.push.payloads()))
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "b second", "2026-10-21T09:00", "")
	f.create(t, "a first", "2026-10-20T09:00", "")
	c := f.create(t, "cancelled", "2026-10-22T09:00", "")
	if _, err := f.svc.Cancel(t.Context(), mustID(c.ID)); err != nil {
		t.Fatal(err)
	}
	texts := func(which string) []string {
		list, err := f.svc.List(t.Context(), which)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range list {
			out = append(out, r.Text)
		}
		return out
	}
	if got := texts(""); !slices.Equal(got, []string{"a first", "b second"}) {
		t.Errorf("upcoming = %q", got)
	}
	if got := texts(ListPast); !slices.Equal(got, []string{"cancelled"}) {
		t.Errorf("past = %q", got)
	}
	if got := texts(ListAll); !slices.Equal(got, []string{"a first", "b second", "cancelled"}) {
		t.Errorf("all = %q", got)
	}
	_ = a
	if _, err := f.svc.List(t.Context(), "weird"); err == nil {
		t.Error("bad filter accepted")
	}
}

// waitFor polls until cond holds; the scheduler runs in its own goroutine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRunSleepsWakesAndFires(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Nothing scheduled: it idles on the maximum sleep.
	waitFor(t, "idle wait", func() bool { return f.clock.lastWait() == maxSleep })

	// A new reminder wakes it, and it sleeps exactly until the reminder is due.
	r := f.create(t, "Stretch", "2026-10-19T14:00:30", "")
	waitFor(t, "a wait until the reminder", func() bool { return f.clock.lastWait() == 30*time.Second })
	if len(f.events.notices()) != 0 {
		t.Fatal("fired before its time")
	}

	f.clock.advance(30 * time.Second)
	waitFor(t, "the reminder to fire", func() bool { return len(f.events.notices()) == 1 })
	waitFor(t, "status fired", func() bool { return f.get(t, r.ID).Status == StatusFired })

	// A reminder added while it sleeps on a later one is still on time.
	f.create(t, "Far", "2026-10-19T20:00", "")
	f.create(t, "Soon", "2026-10-19T14:05", "")
	// Sleeps are capped at a minute so a wall clock jump cannot hide a reminder.
	waitFor(t, "a capped wait", func() bool { return f.clock.lastWait() == maxSleep })
	f.clock.set(f.at("2026-10-19T14:05"))
	waitFor(t, "the second to fire", func() bool { return len(f.events.notices()) == 2 })
	if f.events.notices()[1].Body != "Soon" {
		t.Errorf("notices = %+v", f.events.notices())
	}
}

func TestRunFiresMissedOnStartup(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "Missed", "2026-10-19T15:00", "")
	f.forceAt(t, r.ID, f.at("2026-10-19T12:00"))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "the missed reminder", func() bool { return len(f.events.notices()) == 1 })
}
