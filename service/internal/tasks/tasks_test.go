package tasks

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/when"
)

// recEvents records emitted events.
type recEvents struct {
	mu          sync.Mutex
	invalidated [][]string
}

func (r *recEvents) Invalidate(domains ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, domains)
}
func (r *recEvents) Notify(proto.Notify) {}

func (r *recEvents) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.invalidated)
}

type fixture struct {
	svc    *Service
	events *recEvents
	loc    *time.Location
	now    time.Time // mutable: tests move the clock
	mu     sync.Mutex
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) setNow(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// newFixture starts on Monday 2026-10-19 14:00 in America/Chicago, a week
// before the clocks go back.
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
	f := &fixture{events: &recEvents{}, loc: loc, now: time.Date(2026, 10, 19, 14, 0, 0, 0, loc)}
	f.svc, err = New(Options{Store: db, Location: loc, Events: f.events, Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func ptr[T any](v T) *T { return &v }

func mustCreate(t *testing.T, s *Service, n NewTask) Task {
	t.Helper()
	task, err := s.Create(t.Context(), n)
	if err != nil {
		t.Fatalf("Create(%+v): %v", n, err)
	}
	return task
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status {
		t.Fatalf("err = %v, want status %d", err, status)
	}
}

func TestCreateDefaultsAndValidation(t *testing.T) {
	f := newFixture(t)
	task := mustCreate(t, f.svc, NewTask{Title: "  Buy stamps  "})
	if task.Title != "Buy stamps" || task.List != "Inbox" || task.Due != nil || task.Priority != 0 || task.CompletedAt != nil {
		t.Errorf("task = %+v", task)
	}
	if task.CreatedAt != "2026-10-19T14:00:00-05:00" {
		t.Errorf("created_at = %s", task.CreatedAt)
	}

	for name, n := range map[string]NewTask{
		"no title":     {Title: "  "},
		"long title":   {Title: string(make([]rune, 301))},
		"bad priority": {Title: "x", Priority: 4},
		"bad due":      {Title: "x", Due: "next tuesday"},
		"bad rrule":    {Title: "x", RRule: "EVERY MONDAY"},
		"long list":    {Title: "x", List: string(make([]rune, 61))},
	} {
		if _, err := f.svc.Create(t.Context(), n); err == nil {
			t.Errorf("%s: accepted", name)
		} else {
			wantStatus(t, err, http.StatusBadRequest)
		}
	}
	if got := f.events.count(); got != 1 {
		t.Errorf("invalidate events = %d, want only the one successful create", got)
	}
}

func TestListsAreCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f.svc, NewTask{Title: "Oat milk", List: "Groceries"})
	b := mustCreate(t, f.svc, NewTask{Title: "Eggs", List: "groceries"})
	if b.List != "Groceries" {
		t.Errorf("list = %q, want the existing spelling", b.List)
	}
	mustCreate(t, f.svc, NewTask{Title: "Call mum"})
	done := mustCreate(t, f.svc, NewTask{Title: "Bread", List: "GROCERIES"})
	if _, err := f.svc.Complete(t.Context(), mustID(t, done)); err != nil {
		t.Fatal(err)
	}

	lists, err := f.svc.Lists(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []ListInfo{{"Groceries", 2}, {"Inbox", 1}}
	if !slices.Equal(lists, want) {
		t.Errorf("lists = %+v, want %+v", lists, want)
	}
	tasks, err := f.svc.List(t.Context(), Filter{List: "gRoCeRiEs"})
	if err != nil || len(tasks) != 2 {
		t.Errorf("filter by list: %v %+v", err, tasks)
	}
	moved, err := f.svc.Update(t.Context(), mustID(t, b), Patch{List: ptr("groceries")})
	if err != nil || moved.List != "Groceries" {
		t.Errorf("move keeps spelling: %v %+v", err, moved)
	}
}

func mustID(t *testing.T, task Task) int64 {
	t.Helper()
	var id int64
	for _, c := range task.ID {
		id = id*10 + int64(c-'0')
	}
	return id
}

func TestDueIsReadInTheConfiguredZone(t *testing.T) {
	f := newFixture(t)
	cases := map[string]string{
		"2026-10-20":                "2026-10-20",
		"2026-10-20T09:00":          "2026-10-20T09:00:00-05:00",
		"2026-11-02T09:00":          "2026-11-02T09:00:00-06:00",
		"2026-10-20T14:00:00Z":      "2026-10-20T09:00:00-05:00",
		"2026-10-20T09:00:00+02:00": "2026-10-20T02:00:00-05:00",
	}
	for in, want := range cases {
		task := mustCreate(t, f.svc, NewTask{Title: in, Due: in})
		if str(task.Due) != want {
			t.Errorf("due %q stored as %s, want %s", in, str(task.Due), want)
		}
	}
}

func TestListOrderAndFilters(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f.svc, NewTask{Title: "undated low"})
	mustCreate(t, f.svc, NewTask{Title: "undated high", Priority: 3})
	mustCreate(t, f.svc, NewTask{Title: "later", Due: "2026-10-25"})
	mustCreate(t, f.svc, NewTask{Title: "tomorrow low", Due: "2026-10-20", Priority: 1})
	mustCreate(t, f.svc, NewTask{Title: "tomorrow high", Due: "2026-10-20", Priority: 3})
	mustCreate(t, f.svc, NewTask{Title: "tomorrow morning", Due: "2026-10-20T08:00"})

	titles := func(ts []Task, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range ts {
			out = append(out, x.Title)
		}
		return out
	}
	got := titles(f.svc.List(t.Context(), Filter{}))
	want := []string{"tomorrow high", "tomorrow low", "tomorrow morning", "later", "undated high", "undated low"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}

	// due_before is exclusive and a date means the start of that day.
	before := mustParse(t, f, "2026-10-21")
	got = titles(f.svc.List(t.Context(), Filter{DueBefore: &before}))
	if !slices.Equal(got, []string{"tomorrow high", "tomorrow low", "tomorrow morning"}) {
		t.Errorf("due before 10-21 = %q", got)
	}
	before = mustParse(t, f, "2026-10-20")
	if got = titles(f.svc.List(t.Context(), Filter{DueBefore: &before})); len(got) != 0 {
		t.Errorf("due before 10-20 = %q, want none", got)
	}
	if _, err := f.svc.List(t.Context(), Filter{Status: "weird"}); err == nil {
		t.Error("bad status accepted")
	}
}

func TestCompleteOneOffReopenDelete(t *testing.T) {
	f := newFixture(t)
	task := mustCreate(t, f.svc, NewTask{Title: "File taxes"})
	id := mustID(t, task)

	done, err := f.svc.Complete(t.Context(), id)
	if err != nil || done.CompletedAt == nil || *done.CompletedAt != "2026-10-19T14:00:00-05:00" {
		t.Fatalf("complete: %v %+v", err, done)
	}
	if open, _ := f.svc.List(t.Context(), Filter{}); len(open) != 0 {
		t.Errorf("done task still open: %+v", open)
	}
	if all, _ := f.svc.List(t.Context(), Filter{Status: StatusDone}); len(all) != 1 {
		t.Errorf("done list = %+v", all)
	}
	again, err := f.svc.Complete(t.Context(), id) // idempotent
	if err != nil || str(again.CompletedAt) != str(done.CompletedAt) {
		t.Errorf("second complete: %v %+v", err, again)
	}
	re, err := f.svc.Reopen(t.Context(), id)
	if err != nil || re.CompletedAt != nil {
		t.Errorf("reopen: %v %+v", err, re)
	}

	if _, err := f.svc.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Get(t.Context(), id)
	wantStatus(t, err, http.StatusNotFound)
	_, err = f.svc.Delete(t.Context(), id)
	wantStatus(t, err, http.StatusNotFound)
	_, err = f.svc.Complete(t.Context(), id)
	wantStatus(t, err, http.StatusNotFound)
	// Soft delete: the row is still there.
	var n int
	if err := f.svc.db.QueryRow(`SELECT count(*) FROM tasks WHERE id = ? AND deleted_at IS NOT NULL`, id).Scan(&n); err != nil || n != 1 {
		t.Errorf("soft-deleted rows = %d, err %v", n, err)
	}
	// create, complete, complete(no-op still emits), reopen, delete = 5 changes
	if got := f.events.count(); got != 5 {
		t.Errorf("invalidate events = %d", got)
	}
}

func TestRepeatingTaskCompletion(t *testing.T) {
	type step struct {
		now  string // set the clock first
		want string // due after completing
	}
	tests := []struct {
		name   string
		task   NewTask
		wantID string // due right after creation
		steps  []step
	}{
		{
			// Today is Monday, so "every Monday" starts today.
			name: "weekly all-day starts today", task: NewTask{Title: "Recycling", RRule: "FREQ=WEEKLY;BYDAY=MO"}, wantID: "2026-10-19",
			steps: []step{{"2026-10-19T14:00", "2026-10-26"}, {"2026-10-26T20:00", "2026-11-02"}},
		},
		{
			name: "early completion moves past the current due", task: NewTask{Title: "Bills", Due: "2026-10-30", RRule: "FREQ=MONTHLY;BYMONTHDAY=30,1"},
			wantID: "2026-10-30", steps: []step{{"2026-10-19T14:00", "2026-11-01"}},
		},
		{
			name: "overdue all-day lands after today", task: NewTask{Title: "Plants", Due: "2026-10-05", RRule: "FREQ=WEEKLY;BYDAY=MO"},
			wantID: "2026-10-05", steps: []step{{"2026-10-19T14:00", "2026-10-26"}},
		},
		{
			name: "overdue by days, daily", task: NewTask{Title: "Pills", Due: "2026-10-15", RRule: "FREQ=DAILY"},
			wantID: "2026-10-15", steps: []step{{"2026-10-19T14:00", "2026-10-20"}},
		},
		{
			name: "timed task keeps its time across the DST change", task: NewTask{Title: "Standup", Due: "2026-10-26T09:00", RRule: "FREQ=WEEKLY;BYDAY=MO"},
			wantID: "2026-10-26T09:00:00-05:00", steps: []step{{"2026-10-26T09:05", "2026-11-02T09:00:00-06:00"}},
		},
		{
			name: "timed overdue lands after now", task: NewTask{Title: "Water", Due: "2026-10-19T09:00", RRule: "FREQ=DAILY"},
			wantID: "2026-10-19T09:00:00-05:00", steps: []step{{"2026-10-19T14:00", "2026-10-20T09:00:00-05:00"}},
		},
		{
			name: "timed due later today is not overdue", task: NewTask{Title: "Call", Due: "2026-10-19T18:00", RRule: "FREQ=DAILY"},
			wantID: "2026-10-19T18:00:00-05:00", steps: []step{{"2026-10-19T14:00", "2026-10-20T18:00:00-05:00"}},
		},
		{
			name: "spring forward keeps wall time", task: NewTask{Title: "Run", Due: "2027-03-13T07:30", RRule: "FREQ=DAILY"},
			wantID: "2027-03-13T07:30:00-06:00", steps: []step{{"2027-03-13T08:00", "2027-03-14T07:30:00-05:00"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			task := mustCreate(t, f.svc, tt.task)
			if str(task.Due) != tt.wantID {
				t.Fatalf("initial due = %s, want %s", str(task.Due), tt.wantID)
			}
			for i, st := range tt.steps {
				f.setNow(mustParse(t, f, st.now).T)
				done, err := f.svc.Complete(t.Context(), mustID(t, task))
				if err != nil {
					t.Fatal(err)
				}
				if done.CompletedAt != nil || str(done.Due) != st.want {
					t.Errorf("step %d: due = %s completed=%v, want %s open", i, str(done.Due), done.CompletedAt, st.want)
				}
			}
			var n int
			if err := f.svc.db.QueryRow(`SELECT count(*) FROM task_completions WHERE task_id = ?`, mustID(t, task)).Scan(&n); err != nil || n != len(tt.steps) {
				t.Errorf("completions recorded = %d, want %d (%v)", n, len(tt.steps), err)
			}
		})
	}
}

func mustParse(t *testing.T, f *fixture, s string) (v when.Value) {
	t.Helper()
	v, err := when.Parse(s, f.loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRepeatEndsWithItsRule(t *testing.T) {
	f := newFixture(t)
	task := mustCreate(t, f.svc, NewTask{Title: "Twice", Due: "2026-10-19", RRule: "FREQ=DAILY;COUNT=2"})
	id := mustID(t, task)
	first, _ := f.svc.Complete(t.Context(), id)
	if first.CompletedAt != nil || str(first.Due) != "2026-10-20" {
		t.Fatalf("first = %+v", first)
	}
	f.setNow(f.now.AddDate(0, 0, 1))
	last, err := f.svc.Complete(t.Context(), id)
	if err != nil || last.CompletedAt == nil {
		t.Fatalf("last = %+v %v", last, err)
	}
}

func TestUpdate(t *testing.T) {
	f := newFixture(t)
	task := mustCreate(t, f.svc, NewTask{Title: "Draft", List: "Work", Due: "2026-10-22", Priority: 1})
	id := mustID(t, task)

	up, err := f.svc.Update(t.Context(), id, Patch{Title: ptr("Final draft"), Priority: ptr(3), Notes: ptr("by noon")})
	if err != nil || up.Title != "Final draft" || up.Priority != 3 || up.Notes != "by noon" || up.List != "Work" || str(up.Due) != "2026-10-22" {
		t.Fatalf("update: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{Due: ptr("")})
	if err != nil || up.Due != nil {
		t.Fatalf("clear due: %v %+v", err, up)
	}
	// Adding a rule to a task without a due starts at the first occurrence.
	up, err = f.svc.Update(t.Context(), id, Patch{RRule: ptr("FREQ=WEEKLY;BYDAY=WE")})
	if err != nil || str(up.Due) != "2026-10-21" || str(up.RRule) != "FREQ=WEEKLY;BYDAY=WE" {
		t.Fatalf("add rule: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{RRule: ptr("")})
	if err != nil || up.RRule != nil || str(up.Due) != "2026-10-21" {
		t.Fatalf("clear rule: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{Done: ptr(true)})
	if err != nil || up.CompletedAt == nil {
		t.Fatalf("done: %v %+v", err, up)
	}
	up, err = f.svc.Update(t.Context(), id, Patch{Done: ptr(false)})
	if err != nil || up.CompletedAt != nil {
		t.Fatalf("reopen: %v %+v", err, up)
	}

	_, err = f.svc.Update(t.Context(), id, Patch{Title: ptr(" ")})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.Update(t.Context(), id, Patch{Due: ptr("someday")})
	wantStatus(t, err, http.StatusBadRequest)
	_, err = f.svc.Update(t.Context(), 9999, Patch{Title: ptr("x")})
	wantStatus(t, err, http.StatusNotFound)
	if got, _ := f.svc.Get(context.Background(), id); got.Title != "Final draft" {
		t.Errorf("failed updates changed the task: %+v", got)
	}
}

// A repeating task whose rule sets the time of day keeps that time: completing
// the 09:00 occurrence moves it to tomorrow at 09:00, not back to today.
func TestTimeOfDayRuleKeepsTheTime(t *testing.T) {
	f := newFixture(t)
	f.setNow(time.Date(2026, 10, 20, 8, 0, 0, 0, f.svc.loc))
	task := mustCreate(t, f.svc, NewTask{Title: "Pills", RRule: "FREQ=DAILY;BYHOUR=9;BYMINUTE=0"})
	if task.Due == nil || *task.Due != "2026-10-20T09:00:00-05:00" {
		t.Fatalf("due = %v, want today 09:00 with its time", task.Due)
	}
	f.setNow(time.Date(2026, 10, 20, 9, 30, 0, 0, f.svc.loc))
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	done, err := f.svc.Complete(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if done.Due == nil || *done.Due != "2026-10-21T09:00:00-05:00" {
		t.Errorf("after completing, due = %v, want tomorrow 09:00", done.Due)
	}

	// A date-only due can't carry the rule's time.
	if _, err := f.svc.Create(t.Context(), NewTask{Title: "x", Due: "2026-10-21", RRule: "FREQ=DAILY;BYHOUR=9"}); err == nil {
		t.Error("date-only due with a time-of-day rule accepted")
	}
}
