package routines

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/mail"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/tasks"
)

type fakeTasks struct{ list []tasks.Task }

func (f *fakeTasks) List(_ context.Context, flt tasks.Filter) ([]tasks.Task, error) {
	if flt.Status != tasks.StatusOpen || flt.DueBefore == nil {
		return nil, errors.New("unexpected filter")
	}
	return f.list, nil
}

type fakeCal struct {
	events []calendar.Event
	err    error
}

func (f *fakeCal) Configured() bool { return true }
func (f *fakeCal) Events(context.Context, time.Time, time.Time) ([]calendar.Event, error) {
	return f.events, f.err
}

type fakeMail struct{ attention []mail.Message }

func (f *fakeMail) Configured() bool { return true }
func (f *fakeMail) Summary(context.Context, int) (mail.Summary, error) {
	return mail.Summary{Attention: f.attention}, nil
}

type fakeFollowUps struct {
	due         []memory.Memory
	respectSent bool // honor the limit like the real query: oldest first
	limits      []int
}

func (f *fakeFollowUps) FollowUpsDue(_ context.Context, _ time.Time, limit int) ([]memory.Memory, error) {
	f.limits = append(f.limits, limit)
	if f.respectSent && limit < len(f.due) {
		return f.due[:limit], nil
	}
	return f.due, nil
}

type recorder struct {
	mu          sync.Mutex
	notices     []proto.Notify
	pushes      []push.Payload
	invalidated []string
}

func (r *recorder) Invalidate(d ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, d...)
}

func (r *recorder) Notify(n proto.Notify) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, n)
}

func (r *recorder) Send(_ context.Context, p push.Payload, _ push.Urgency) (push.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = append(r.pushes, p)
	return push.Result{Sent: 1}, nil
}

type fixture struct {
	svc       *Service
	rec       *recorder
	tasks     *fakeTasks
	cal       *fakeCal
	mail      *fakeMail
	followUps *fakeFollowUps
	loc       *time.Location
	now       time.Time
}

// newFixture starts on Tuesday 2026-10-20 14:00 in America/Chicago.
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
	f := &fixture{rec: &recorder{}, tasks: &fakeTasks{}, cal: &fakeCal{}, mail: &fakeMail{}, followUps: &fakeFollowUps{},
		loc: loc, now: time.Date(2026, 10, 20, 14, 0, 0, 0, loc)}
	f.svc, err = New(Options{Store: db, Tasks: f.tasks, Calendar: f.cal, Mail: f.mail, FollowUps: f.followUps,
		Location: loc, Events: f.rec, Push: f.rec, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func str(s string) *string { return &s }

func TestHeartbeatNudgesOnceAboutEach(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	at := func(h, m int) string { return time.Date(2026, 10, 20, h, m, 0, 0, f.loc).Format(time.RFC3339) }
	f.cal.events = []calendar.Event{
		{UID: "dentist", Title: "Dentist", Start: at(14, 45), Location: "12 Main St"},
		{UID: "standup", Title: "Standup", Start: at(14, 30)},                                // no place to get to
		{UID: "holiday", Title: "Holiday", Start: "2026-10-20", AllDay: true, Location: "x"}, // all day
	}
	f.tasks.list = []tasks.Task{
		{ID: "1", Title: "Pay rent", Due: str("2026-10-19")},
		{ID: "2", Title: "Buy milk", Due: str("2026-10-20")}, // due today: not late yet
		{ID: "3", Title: "Call the bank", Due: str(at(11, 0))},
	}
	f.mail.attention = []mail.Message{
		{ID: "381", From: mail.Address{Name: "Landlord"}, Subject: "Lease renewal", Date: at(13, 0)},
		{ID: "382", From: mail.Address{Address: "boss@example.com"}, Subject: "Just now", Date: at(14, 25)}, // give them time
	}
	f.followUps.due = []memory.Memory{{ID: "5", Content: "Ask how the interview went", FollowUpAt: str(at(9, 0))}}

	summary, err := f.svc.Heartbeat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Nudged about 5 thing(s)" {
		t.Errorf("summary = %q", summary)
	}
	if len(f.rec.notices) != 1 || len(f.rec.pushes) != 1 {
		t.Fatalf("notices %d, pushes %d", len(f.rec.notices), len(f.rec.pushes))
	}
	n := f.rec.notices[0]
	want := "Dentist at 2:45 PM, 12 Main St (in 45 min)\n2 overdue: Pay rent; Call the bank\nNeeds a reply or a look: Landlord: Lease renewal\nOn my mind: Ask how the interview went"
	if n.Kind != proto.NotifyKindNudge || n.Title != "A few things" || n.Body != want {
		t.Errorf("notice = %q / %q", n.Title, n.Body)
	}
	if p := f.rec.pushes[0]; p.Body != want || p.URL != "/?menu=today" || p.Tag != "nudge" {
		t.Errorf("push = %+v", p)
	}

	// Half an hour later nothing is new: a quiet run, no repeats.
	f.now = f.now.Add(30 * time.Minute)
	if summary, err := f.svc.Heartbeat(ctx); err != nil || summary != "Nothing worth a nudge" {
		t.Errorf("second run: %q %v", summary, err)
	}
	// The mail that just arrived has now waited long enough.
	f.now = f.now.Add(30 * time.Minute)
	if _, err := f.svc.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.rec.notices[len(f.rec.notices)-1]; len(f.rec.notices) != 2 || got.Title != "Mail that needs you" || got.Body != "Needs a reply or a look: boss@example.com: Just now" {
		t.Errorf("third run notice = %+v", got)
	}
}

func TestHeartbeatRespectsSettingsAndFailures(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.followUps.due = []memory.Memory{{ID: "5", Content: "Ask about the trip"}}

	f.now = time.Date(2026, 10, 20, 23, 0, 0, 0, f.loc) // after 22:00
	if _, err := f.svc.Heartbeat(ctx); !errors.Is(err, jobs.ErrSkip) {
		t.Errorf("outside waking hours: %v", err)
	}
	if _, err := f.svc.Update(ctx, Patch{WakingStart: str("09:00"), WakingEnd: str("01:00")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Heartbeat(ctx); err != nil || len(f.rec.notices) != 1 {
		t.Errorf("waking hours past midnight: %v, %d notices", err, len(f.rec.notices))
	}
	off := false
	if _, err := f.svc.Update(ctx, Patch{Heartbeat: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Heartbeat(ctx); !errors.Is(err, jobs.ErrSkip) {
		t.Errorf("switched off: %v", err)
	}
	if _, err := f.svc.Update(ctx, Patch{WakingStart: str("7am")}); err == nil {
		t.Error("a bad time should be refused")
	}
	if _, err := f.svc.Update(ctx, Patch{WakingStart: str("01:00")}); err == nil {
		t.Error("an empty waking window should be refused")
	}

	on := true
	if _, err := f.svc.Update(ctx, Patch{Heartbeat: &on}); err != nil {
		t.Fatal(err)
	}
	f.cal.err = errors.New("calendar down")
	f.followUps.due = append(f.followUps.due, memory.Memory{ID: "6", Content: "Ask about the dentist"})
	summary, err := f.svc.Heartbeat(ctx)
	if err != nil || summary != "Nudged about 1 thing(s) (couldn't check calendar)" {
		t.Errorf("one check failing: %q %v", summary, err)
	}
	if got := f.rec.notices[len(f.rec.notices)-1]; got.Title != "When you have a moment" || got.Body != "On my mind: Ask about the dentist" {
		t.Errorf("single nudge = %+v", got)
	}
	if !strings.Contains(strings.Join(f.rec.invalidated, ","), DomainRoutines) {
		t.Errorf("settings changes invalidate %q: %v", DomainRoutines, f.rec.invalidated)
	}
}

func TestComposeCapsLines(t *testing.T) {
	var list []nudge
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		list = append(list, nudge{key: s, kind: "follow_up", title: "t", line: s})
	}
	title, body, shown := compose(list)
	if title != "A few things" || body != "On my mind: a\nOn my mind: b\nOn my mind: c\nand 2 more" || len(shown) != 3 {
		t.Errorf("%q %q %d", title, body, len(shown))
	}
}

func TestSettingsREST(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/routines")
	if err != nil {
		t.Fatal(err)
	}
	var st Settings
	_ = json.NewDecoder(res.Body).Decode(&st)
	_ = res.Body.Close()
	if st != defaults {
		t.Errorf("defaults = %+v", st)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, srv.URL+"/api/routines", strings.NewReader(`{"waking_start":"7:30"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %v %v", res.StatusCode, err)
	}
	_ = json.NewDecoder(res.Body).Decode(&st)
	_ = res.Body.Close()
	if st.WakingStart != "07:30" || !st.Heartbeat {
		t.Errorf("patched = %+v", st)
	}
}

func TestNudgesLeftOutComeNextTime(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for i := range 7 {
		f.followUps.due = append(f.followUps.due, memory.Memory{ID: strconv.Itoa(i + 1), Content: "Ask about thing " + strconv.Itoa(i+1)})
	}
	f.followUps.respectSent = true
	if summary, err := f.svc.Heartbeat(ctx); err != nil || summary != "Nudged about 3 thing(s)" {
		t.Fatalf("first: %q %v", summary, err)
	}
	f.now = f.now.Add(30 * time.Minute)
	if summary, err := f.svc.Heartbeat(ctx); err != nil || summary != "Nudged about 4 thing(s)" {
		t.Fatalf("second: %q %v", summary, err)
	}
	if got := f.rec.notices[1].Body; !strings.Contains(got, "thing 4") || !strings.Contains(got, "thing 7") || strings.Contains(got, "thing 1") {
		t.Errorf("second notice = %q", got)
	}
	f.now = f.now.Add(30 * time.Minute)
	if summary, _ := f.svc.Heartbeat(ctx); summary != "Nothing worth a nudge" {
		t.Errorf("third: %q", summary)
	}
	if f.followUps.limits[0] < 50 {
		t.Errorf("follow-ups fetched %d at a time: already-nudged ones would crowd out the rest", f.followUps.limits[0])
	}
}

func TestLongLinesAreNamedInFull(t *testing.T) {
	var list []nudge
	for i := range 3 {
		list = append(list, nudge{key: strconv.Itoa(i), kind: "mail", line: strings.Repeat("x", maxLine-1) + strconv.Itoa(i)})
	}
	_, body, shown := compose(list)
	if len(shown) != 3 || !strings.Contains(body, "x2") {
		t.Errorf("shown %d, body ends %q", len(shown), body[len(body)-20:])
	}
}
