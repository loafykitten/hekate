package reflection

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

// fakeModel answers with a scripted reply and keeps what it was asked.
type fakeModel struct {
	mu    sync.Mutex
	reply string
	err   error
	asked []llm.Request
}

func (m *fakeModel) Configured() bool { return true }
func (m *fakeModel) Complete(_ context.Context, req llm.Request) (string, *llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, req)
	cost := 0.001
	return m.reply, &llm.Result{Model: req.Model, Usage: llm.Usage{PromptTokens: 1000, CompletionTokens: 200}, CostUSD: &cost}, m.err
}

type recEvents struct {
	mu   sync.Mutex
	seen []string
}

func (r *recEvents) Invalidate(d ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, d...)
}
func (r *recEvents) Notify(proto.Notify) {}

type fixture struct {
	t      *testing.T
	store  *store.Store
	mem    *memory.Service
	svc    *Service
	model  *fakeModel
	events *recEvents
	loc    *time.Location
	now    time.Time
	turns  int
}

// newFixture starts at Tuesday 2026-10-20 03:30 in America/Chicago.
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
	if err := db.EnsureConversation(t.Context(), "default"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: db, model: &fakeModel{}, events: &recEvents{}, loc: loc, now: time.Date(2026, 10, 20, 3, 30, 0, 0, loc)}
	clock := func() time.Time { return f.now }
	f.mem, err = memory.New(memory.Options{Store: db, Location: loc, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.svc, err = New(Options{Store: db, Memory: f.mem, LLM: f.model, Model: config.Model{Name: "flex"}, Tamer: "Rika",
		Location: loc, Events: f.events, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// say records an exchange at the given local time.
func (f *fixture) say(at time.Time, user, reply string) {
	f.t.Helper()
	f.turns++
	id := "t" + strconv.Itoa(f.turns)
	ctx := f.t.Context()
	if err := f.store.BeginTurn(ctx, store.Turn{ID: id, ConversationID: "default", StartedAt: at}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.AddMessages(ctx, []store.Message{
		{ConversationID: "default", TurnID: id, Role: "user", Text: user, Content: user, CreatedAt: at},
		{ConversationID: "default", TurnID: id, Role: "assistant", Text: reply, Content: reply, CreatedAt: at.Add(time.Second)},
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remember(kind, content string) int64 {
	f.t.Helper()
	m, _, err := f.mem.Save(f.t.Context(), memory.NewMemory{Kind: kind, Content: content})
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := strconv.ParseInt(m.ID, 10, 64)
	return id
}

func TestNothingToReflectOn(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Reflect(t.Context()); !errors.Is(err, jobs.ErrSkip) {
		t.Errorf("no conversation: %v", err)
	}
	if len(f.model.asked) != 0 {
		t.Error("the model was asked about nothing")
	}
}

func TestReflectAppliesAndRecords(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	old := f.remember("fact", "Has a job interview on Friday")
	dupA := f.remember("preference", "Likes oolong")
	dupB := f.remember("preference", "Drinks oolong tea most mornings")
	f.remember("person", "Mika is Rika's sister")
	f.say(time.Date(2026, 10, 19, 9, 15, 0, 0, f.loc), "The interview got moved to Thursday at 2, ugh.", "Thursday it is. You'll be ready.")
	f.say(time.Date(2026, 10, 19, 21, 40, 0, 0, f.loc), "Mika's coming over Saturday. Also I'm off coffee now.", "Noted. I'll ask how it went with Mika.")
	f.say(time.Date(2026, 10, 18, 20, 0, 0, 0, f.loc), "too old to read", "and this") // before the window

	f.model.reply = "Here you go:\n```json\n" + `{
		"save": [
			{"kind": "preference", "content": "Has given up coffee", "importance": 3, "reason": "said so tonight"},
			{"kind": "follow_up", "content": "Ask how Mika's visit went", "follow_up_at": "2026-10-25", "reason": "visit Saturday"}
		],
		"update": [
			{"id": "` + strconv.FormatInt(old, 10) + `", "content": "Has a job interview on Thursday at 2 pm", "kind": "follow_up", "follow_up_at": "2026-10-23T17:00", "reason": "moved"},
			{"id": ` + strconv.FormatInt(dupB, 10) + `, "content": "Likes oolong tea, most mornings", "reason": "merged with a duplicate"}
		],
		"retire": [{"id": ` + strconv.FormatInt(dupA, 10) + `, "reason": "merged into the other"}],
		"diary": "Rika's interview moved. They sounded tired, but steadier than last week.",
		"summary": ""
	}` + "\n```"
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Saved 2, updated 2 and retired 1 memories; wrote the diary for 2026-10-19" {
		t.Errorf("summary = %q", summary)
	}

	// What the model read.
	req := f.model.asked[0]
	if sys := req.Messages[0].Content; req.Model != "flex" || !strings.Contains(sys, "reflect on Monday, October 19 with Rika") || !strings.Contains(sys, "You are Renamon") {
		t.Errorf("system prompt: %q", sys)
	}
	input := req.Messages[1].Content
	for _, want := range []string{"Rika: The interview got moved to Thursday", "You: Noted.", "id " + strconv.FormatInt(dupA, 10) + ", preference, importance 3: Likes oolong", "<<<DATA "} {
		if !strings.Contains(input, want) {
			t.Errorf("input lacks %q", want)
		}
	}
	if strings.Contains(input, "too old to read") {
		t.Error("the input reaches before the window")
	}

	// What changed.
	m, err := f.mem.Get(ctx, old)
	if err != nil || m.Kind != "follow_up" || m.Content != "Has a job interview on Thursday at 2 pm" || m.FollowUpAt == nil || !strings.HasPrefix(*m.FollowUpAt, "2026-10-23T17:00") {
		t.Errorf("updated = %+v %v", m, err)
	}
	if _, err := f.mem.Get(ctx, dupA); err == nil {
		t.Error("the duplicate is still live")
	}
	if found, _ := f.mem.Search(ctx, "coffee", "", 5); len(found) != 1 {
		t.Errorf("saved: %+v", found)
	}

	days, err := f.svc.Journal(ctx, 0)
	if err != nil || len(days) != 1 {
		t.Fatalf("journal = %+v %v", days, err)
	}
	d := days[0]
	if d.Day != "2026-10-19" || d.Diary == nil || !strings.Contains(d.Diary.Text, "interview moved") || len(d.Reflections) != 1 {
		t.Fatalf("day = %+v", d)
	}
	ch := d.Reflections[0].Changes
	if len(ch) != 5 || ch[0].Action != ActionUpdate || ch[0].Before.Content != "Has a job interview on Friday" || ch[2].Action != ActionRetire || ch[3].Action != ActionSave {
		t.Fatalf("changes = %+v", ch)
	}
	if d.Reflections[0].Summary != summary {
		t.Errorf("stored summary = %q", d.Reflections[0].Summary)
	}

	// Undo each kind of change.
	if _, err := f.svc.Undo(ctx, mustID(t, ch[0].ID)); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.mem.Get(ctx, old); m.Content != "Has a job interview on Friday" || m.Kind != "fact" || m.FollowUpAt != nil {
		t.Errorf("undone update = %+v", m)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, ch[2].ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Get(ctx, dupA); err != nil {
		t.Errorf("undone retirement: %v", err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, ch[3].ID)); err != nil {
		t.Fatal(err)
	}
	if found, _ := f.mem.Search(ctx, "coffee", "", 5); len(found) != 0 {
		t.Errorf("undone save: %+v", found)
	}
	var he *httpx.Error
	if _, err := f.svc.Undo(ctx, mustID(t, ch[3].ID)); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("undoing twice: %v", err)
	}
	// Forgetting a memory forgets the copies the reflection kept of it.
	if _, err := f.mem.Forget(ctx, dupB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, ch[1].ID)); !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Errorf("undoing a change to a forgotten memory: %v", err)
	}
	days, _ = f.svc.Journal(ctx, 0)
	for _, c := range days[0].Reflections[0].Changes {
		if c.MemoryID == strconv.FormatInt(dupB, 10) {
			t.Errorf("a forgotten memory's copy is still in the journal: %+v", c)
		}
	}

	// The next night reads only what came after.
	f.now = f.now.Add(24 * time.Hour)
	if _, err := f.svc.Reflect(ctx); !errors.Is(err, jobs.ErrSkip) {
		t.Errorf("no new conversation: %v", err)
	}
	f.events.mu.Lock()
	defer f.events.mu.Unlock()
	if len(f.events.seen) == 0 {
		t.Error("no invalidate")
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestParsePlan(t *testing.T) {
	if _, err := parsePlan("I can't"); err == nil {
		t.Error("no JSON")
	}
	if _, err := parsePlan(`{"save": [], "diary": "  "}`); err == nil {
		t.Error("no diary")
	}
	if _, err := parsePlan(`{"update": [{"id": "x"}], "diary": "d"}`); err == nil {
		t.Error("a non-numeric id")
	}
	many := `{"retire": [` + strings.Repeat(`{"id": 1},`, 30) + `{"id": 1}], "diary": "d"}`
	p, err := parsePlan(many)
	if err != nil || len(p.Retire) != maxRetires {
		t.Errorf("capped retires = %d, %v", len(p.Retire), err)
	}
}

func TestFailuresAreReported(t *testing.T) {
	f := newFixture(t)
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = "Sorry, I can't do that."
	if _, err := f.svc.Reflect(t.Context()); err == nil {
		t.Error("an answer without JSON should fail the run")
	}
	f.model.reply, f.model.err = "", errors.New("unreachable")
	if _, err := f.svc.Reflect(t.Context()); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("model error: %v", err)
	}
	// Usage is recorded even when the run fails.
	var n int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_usage WHERE source = 'reflection'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("usage rows = %d %v", n, err)
	}
}

func TestDiaryToolContextAndREST(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if out, _ := f.svc.readDiary(ctx, ""); !strings.Contains(out, "haven't written") {
		t.Errorf("empty diary: %s", out)
	}
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "Lunch with Mika was lovely.", "Glad to hear it.")
	f.model.reply = `{"save": [{"kind": "event", "content": "Had lunch with Mika", "reason": "nice day"}], "diary": "A gentle day. Rika laughed at lunch."}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.readDiary(ctx, "")
	if err != nil || !strings.Contains(out, "Your diary, 2026-10-19:\nA gentle day.") || !strings.Contains(out, `saved memory 1, "Had lunch with Mika" (nice day)`) {
		t.Errorf("diary_read:\n%s %v", out, err)
	}
	if out, _ := f.svc.readDiary(ctx, "2026-10-01"); !strings.Contains(out, "no diary entry for 2026-10-01") {
		t.Errorf("missing day: %s", out)
	}
	if _, err := f.svc.readDiary(ctx, "Monday"); err == nil {
		t.Error("a bad day should fail")
	}
	got, _ := f.svc.PromptContext(ctx, f.now.Add(10*time.Hour))
	if !strings.Contains(got, "Your diary entry for Monday, October 19: A gentle day.") {
		t.Errorf("context = %q", got)
	}
	if got, _ := f.svc.PromptContext(ctx, f.now.Add(5*24*time.Hour)); got != "" {
		t.Errorf("stale context = %q", got)
	}

	mux := http.NewServeMux()
	f.svc.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/journal")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("journal: %v", err)
	}
	_ = res.Body.Close()
	days, _ := f.svc.Journal(ctx, 0)
	change := days[0].Reflections[0].Changes[0].ID
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/reflections/changes/"+change+"/undo", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("undo: %v %v", res.StatusCode, err)
	}
	_ = res.Body.Close()
	res, _ = http.DefaultClient.Do(req.Clone(ctx))
	if res.StatusCode != http.StatusConflict {
		t.Errorf("second undo = %d", res.StatusCode)
	}
	_ = res.Body.Close()
}

// editingModel edits a memory while "thinking", as the owner might.
type editingModel struct {
	*fakeModel
	during func()
}

func (m *editingModel) Complete(ctx context.Context, req llm.Request) (string, *llm.Result, error) {
	m.during()
	return m.fakeModel.Complete(ctx, req)
}

func TestEditsMadeMeanwhileWin(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a := f.remember("fact", "Lives on Elm Street")
	b := f.remember("fact", "Drives a blue car")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	model := &editingModel{fakeModel: &fakeModel{reply: `{
		"update": [{"id": ` + strconv.FormatInt(a, 10) + `, "content": "Lives on Oak Street", "reason": "x"}],
		"retire": [{"id": ` + strconv.FormatInt(b, 10) + `, "reason": "x"}],
		"diary": "d"}`}}
	model.during = func() {
		f.now = f.now.Add(2 * time.Second)
		content := "Lives on Maple Street"
		if _, err := f.mem.Update(ctx, a, memory.Patch{Content: &content}); err != nil {
			t.Fatal(err)
		}
		imp := 5
		if _, err := f.mem.Update(ctx, b, memory.Patch{Importance: &imp}); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.llm = model
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "(1 left alone: changed meanwhile) (1 retirements held back: an update didn't happen)") {
		t.Errorf("summary = %q", summary)
	}
	if m, _ := f.mem.Get(ctx, a); m.Content != "Lives on Maple Street" {
		t.Errorf("the owner's edit was overwritten: %+v", m)
	}
	if _, err := f.mem.Get(ctx, b); err != nil {
		t.Errorf("the owner's edited memory was retired: %v", err)
	}
}

func TestUndoRefusesToLoseNewerEdits(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a := f.remember("fact", "Has a cat")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(a, 10) + `, "content": "Has a cat named Miso", "reason": "named"}],
		"save": [{"kind": "fact", "content": "Plays the cello", "reason": "said so"}], "diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	days, _ := f.svc.Journal(ctx, 0)
	ch := days[0].Reflections[0].Changes
	f.now = f.now.Add(5 * time.Second)
	content := "Has a cat named Miso, who is twelve"
	if _, err := f.mem.Update(ctx, a, memory.Patch{Content: &content}); err != nil {
		t.Fatal(err)
	}
	var he *httpx.Error
	if _, err := f.svc.Undo(ctx, mustID(t, ch[0].ID)); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("undoing an update the owner built on: %v", err)
	}
	if m, _ := f.mem.Get(ctx, a); m.Content != content {
		t.Errorf("the newer edit was lost: %+v", m)
	}
	saved := mustID(t, ch[1].MemoryID)
	imp := 4
	if _, err := f.mem.Update(ctx, saved, memory.Patch{Importance: &imp}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, ch[1].ID)); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("undoing a save the owner edited: %v", err)
	}
}

func TestUnfinishedReflectionDoesNotAdvance(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	_, err := f.store.DB().ExecContext(ctx, `INSERT INTO reflections (day, window_start, window_end, created_at) VALUES ('2026-10-19', ?, ?, ?)`,
		stamp(f.now.Add(-24*time.Hour)), stamp(f.now), stamp(f.now))
	if err != nil {
		t.Fatal(err)
	}
	start, err := f.svc.windowStart(ctx, f.now)
	if err != nil || !start.Equal(f.now.Add(-defaultWindow)) {
		t.Errorf("an unfinished reflection moved the window: %v %v", start, err)
	}
}

func TestASaveNeverTouchesAMemorySavedMeanwhile(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "I started cello lessons", "Lovely.")
	model := &editingModel{fakeModel: &fakeModel{reply: `{"save": [{"kind": "fact", "content": "Plays the cello", "importance": 5,
		"follow_up_at": "2026-11-01", "reason": "x"}], "diary": "d"}`}}
	model.during = func() { f.remember("fact", "Plays the cello") } // the owner saved it while she thought
	f.svc.llm = model
	summary, err := f.svc.Reflect(ctx)
	if err != nil || !strings.Contains(summary, "Saved 0") || !strings.Contains(summary, "(1 left alone: changed meanwhile)") {
		t.Fatalf("summary = %q %v", summary, err)
	}
	found, _ := f.mem.Search(ctx, "cello", "", 5)
	if len(found) != 1 || found[0].Importance != 3 || found[0].FollowUpAt != nil {
		t.Errorf("the owner's memory was changed: %+v", found)
	}
	days, _ := f.svc.Journal(ctx, 0)
	if n := len(days[0].Reflections[0].Changes); n != 0 {
		t.Errorf("recorded %d changes", n)
	}
}

func TestMergeKeepsSourcesWhenTheDestinationUpdateIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("preference", "Likes tea")
	src := f.remember("preference", "Likes oolong in the morning")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "kind": "wish", "content": "Likes tea, oolong in the morning", "reason": "merge"}],
		"retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}], "diary": "d"}`
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Get(ctx, src); err != nil {
		t.Errorf("the merge source was retired though the merge failed (%q): %v", summary, err)
	}
}

func TestJunkIsRefusedAndHoldsRetirementsBack(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	keep := f.remember("person", "Mika is Rika's sister")
	src := f.remember("preference", "Likes oolong")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	for _, update := range []string{
		`{"id": 9999, "content": "a mistyped destination", "reason": "merge"}`,
		`{"id": ` + strconv.FormatInt(keep, 10) + `, "reason": "no fields"}`,
	} {
		f.model.reply = `{"update": [` + update + `], "retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}],
			"save": [{"kind": "wish", "content": "bad kind"}], "diary": "d"}`
		summary, err := f.svc.Reflect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(summary, "Saved 0, updated 0 and retired 0 memories") || !strings.HasSuffix(summary, "(2 changes refused) (1 retirements held back: an update didn't happen)") {
			t.Errorf("summary = %q", summary)
		}
		if _, err := f.mem.Get(ctx, src); err != nil {
			t.Errorf("the merge source was retired: %v", err)
		}
		f.say(f.now.Add(time.Hour), "again", "again") // something new for the next run
		f.now = f.now.Add(2 * time.Hour)
	}
}

func TestADatabaseFailureLeavesItUnfinished(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "I play the cello", "Lovely.")
	model := &editingModel{fakeModel: &fakeModel{reply: `{"save": [{"kind": "fact", "content": "Plays the cello"}], "diary": "d"}`}}
	model.during = func() {
		if _, err := f.store.DB().ExecContext(ctx, `DROP TABLE reflection_changes`); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.llm = model
	if _, err := f.svc.Reflect(ctx); err == nil {
		t.Fatal("a failing database should fail the run")
	}
	if found, _ := f.mem.Search(ctx, "cello", "", 5); len(found) != 0 {
		t.Errorf("the save survived its failed record: %+v", found)
	}
	if start, _ := f.svc.windowStart(ctx, f.now); !start.Equal(f.now.Add(-defaultWindow)) {
		t.Errorf("the failed run advanced the window to %v", start)
	}
}

func TestUndoInReverseOrder(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a := f.remember("fact", "Has a cat")
	for i, content := range []string{"Has a cat named Miso", "Has a cat named Miso, who is twelve"} {
		f.say(f.now.Add(-time.Hour), "hi", "hello")
		f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(a, 10) + `, "content": "` + content + `", "reason": "x"}], "diary": "d` + strconv.Itoa(i) + `"}`
		if _, err := f.svc.Reflect(ctx); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(24 * time.Hour)
	}
	days, _ := f.svc.Journal(ctx, 0)
	newest, older := days[0].Reflections[0].Changes[0], days[1].Reflections[0].Changes[0]
	if _, err := f.svc.Undo(ctx, mustID(t, newest.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, older.ID)); err != nil {
		t.Fatalf("undoing the earlier change after the later one: %v", err)
	}
	if m, _ := f.mem.Get(ctx, a); m.Content != "Has a cat" {
		t.Errorf("after both undos: %+v", m)
	}
}

func TestTooManyUpdatesHoldRetirementsBack(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	src := f.remember("preference", "Likes oolong")
	var ids []int64
	for i := range maxUpdates + 1 {
		ids = append(ids, f.remember("fact", "Fact number "+strconv.Itoa(i)))
	}
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	var ups []string
	for i, id := range ids {
		ups = append(ups, `{"id": `+strconv.FormatInt(id, 10)+`, "importance": 4, "reason": "u`+strconv.Itoa(i)+`"}`)
	}
	f.model.reply = `{"update": [` + strings.Join(ups, ",") + `], "retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged into the last one"}], "diary": "d"}`
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Get(ctx, src); err != nil {
		t.Errorf("retired although an update was cut (%q): %v", summary, err)
	}
}

func TestForgettingWhileSheThinksStopsTheRun(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("person", "Mika is Rika's sister")
	secret := f.remember("fact", "A private detail about Mika")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	model := &editingModel{fakeModel: &fakeModel{reply: `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `,
		"content": "Mika is Rika's sister. A private detail about Mika", "reason": "merge"}],
		"retire": [{"id": ` + strconv.FormatInt(secret, 10) + `, "reason": "merged"}], "diary": "d"}`}}
	model.during = func() {
		if _, err := f.mem.Forget(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.llm = model
	if _, err := f.svc.Reflect(ctx); err == nil || !strings.Contains(err.Error(), "forgotten") {
		t.Fatalf("run: %v", err)
	}
	if m, _ := f.mem.Get(ctx, dest); m.Content != "Mika is Rika's sister" {
		t.Errorf("the forgotten words reached another memory: %+v", m)
	}
	if start, _ := f.svc.windowStart(ctx, f.now); !start.Equal(f.now.Add(-defaultWindow)) {
		t.Errorf("the stopped run advanced the window to %v", start)
	}
}

func TestForgettingEitherSideOfAMergeForgetsBoth(t *testing.T) {
	for _, side := range []string{"destination", "source"} {
		t.Run(side, func(t *testing.T) {
			f := newFixture(t)
			ctx := t.Context()
			dest := f.remember("person", "Mika is Rika's sister")
			src := f.remember("fact", "Mika works nights")
			other := f.remember("fact", "Unrelated")
			f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
			f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "content": "Mika is Rika's sister and works nights",
				"merged_from": [` + strconv.FormatInt(src, 10) + `], "reason": "merge"}],
				"retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}], "diary": "d"}`
			if _, err := f.svc.Reflect(ctx); err != nil {
				t.Fatal(err)
			}
			target := dest
			if side == "source" {
				target = src
			}
			if _, err := f.mem.Forget(ctx, target); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := f.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE id IN (?, ?)`, dest, src).Scan(&n); err != nil || n != 0 {
				t.Errorf("merged memories left: %d %v", n, err)
			}
			if err := f.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM reflection_changes`).Scan(&n); err != nil || n != 0 {
				t.Errorf("copies left: %d %v", n, err)
			}
			if _, err := f.mem.Get(ctx, other); err != nil {
				t.Errorf("an unrelated memory went too: %v", err)
			}
		})
	}
}

func TestMemoryIDsAreStrict(t *testing.T) {
	for _, bad := range []string{`"1e3"`, `"010x"`, `"abc"`, `"-4"`, `"0"`} {
		var id flexID
		if err := id.UnmarshalJSON([]byte(bad)); err == nil {
			t.Errorf("%s parsed as %d", bad, id)
		}
	}
	for raw, want := range map[string]flexID{`12`: 12, `"12"`: 12, `" 7 "`: 7, `"010"`: 10} {
		var id flexID
		if err := id.UnmarshalJSON([]byte(raw)); err != nil || id != want {
			t.Errorf("%s = %d, %v", raw, id, err)
		}
	}
}

func TestAMergeIsLinkedEvenWhenItsRetirementIsHeldBack(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("person", "Mika is Rika's sister")
	src := f.remember("fact", "Mika works nights")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [
		{"id": ` + strconv.FormatInt(dest, 10) + `, "content": "Mika is Rika's sister and works nights", "merged_from": [` + strconv.FormatInt(src, 10) + `], "reason": "merge"},
		{"id": 9999, "content": "refused", "reason": "x"}],
		"retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}], "diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Get(ctx, src); err != nil {
		t.Fatalf("the source should be held back: %v", err)
	}
	if _, err := f.mem.Forget(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Get(ctx, dest); err == nil {
		t.Error("the memory holding the forgotten words is still there")
	}
}

func TestInvalidMergeSourcesRefuseTheUpdate(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("person", "Mika is Rika's sister")
	src := f.remember("fact", "Mika works nights")
	for _, from := range []string{"9999", strconv.FormatInt(dest, 10)} {
		f.say(f.now.Add(-time.Minute), "hi", "hello")
		f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "content": "merged", "merged_from": [` + from + `], "reason": "merge"}],
			"retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}], "diary": "d"}`
		if _, err := f.svc.Reflect(ctx); err != nil {
			t.Fatal(err)
		}
		if m, _ := f.mem.Get(ctx, dest); m.Content != "Mika is Rika's sister" {
			t.Errorf("merged_from %s: the update went through: %+v", from, m)
		}
		if _, err := f.mem.Get(ctx, src); err != nil {
			t.Errorf("merged_from %s: the source was retired", from)
		}
		f.now = f.now.Add(time.Hour)
	}
}

func TestForgottenWordsAreNotSavedAgain(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	gone := f.remember("fact", "Owes Mika fifty dollars")
	keep := f.remember("fact", "Likes tea")
	if _, err := f.mem.Forget(ctx, gone); err != nil { // forgotten in the menu, before the reflection
		t.Fatal(err)
	}
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "I still owe Mika fifty dollars", "Noted.")
	f.model.reply = `{"save": [{"kind": "fact", "content": "owes mika fifty dollars.", "reason": "said so"}],
		"update": [{"id": ` + strconv.FormatInt(keep, 10) + `, "content": "Owes Mika fifty dollars", "reason": "x"}], "diary": "d"}`
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "(2 not saved: the owner had them forgotten)") {
		t.Errorf("summary = %q", summary)
	}
	if found, _ := f.mem.Search(ctx, "fifty", "", 5); len(found) != 0 {
		t.Errorf("a forgotten memory came back: %+v", found)
	}
	var stored string
	if err := f.store.DB().QueryRowContext(ctx, `SELECT fingerprint FROM memory_forgotten`).Scan(&stored); err != nil || strings.Contains(stored, "fifty") || len(stored) != 64 {
		t.Errorf("the fingerprint holds the text, or is missing: %q %v", stored, err)
	}
	// The owner saying it again in conversation still saves it: only reflection is held back.
	if _, existed, err := f.mem.Save(ctx, memory.NewMemory{Kind: "fact", Content: "Owes Mika fifty dollars"}); err != nil || existed {
		t.Errorf("an explicit save: %v %v", existed, err)
	}
}

func TestUndoingAMergeUnlinksIt(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("person", "Mika is Rika's sister")
	src := f.remember("fact", "Mika works nights")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "content": "Mika is Rika's sister and works nights",
		"merged_from": [` + strconv.FormatInt(src, 10) + `], "reason": "merge"}],
		"retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}], "diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	days, _ := f.svc.Journal(ctx, 0)
	for _, c := range days[0].Reflections[0].Changes {
		if _, err := f.svc.Undo(ctx, mustID(t, c.ID)); err != nil {
			t.Fatal(err)
		}
	}
	days, _ = f.svc.Journal(ctx, 0)
	if c := days[0].Reflections[0].Changes[0]; c.After != nil {
		t.Errorf("the undone merge still holds the merged words: %+v", c.After)
	}
	if _, err := f.mem.Forget(ctx, src); err != nil {
		t.Fatal(err)
	}
	if m, err := f.mem.Get(ctx, dest); err != nil || m.Content != "Mika is Rika's sister" {
		t.Errorf("forgetting the unmerged source took the other memory too: %+v %v", m, err)
	}
}

func TestForgettingBlocksEarlierWordingsToo(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a := f.remember("fact", "Owes Mika fifty dollars")
	keep := f.remember("fact", "Likes tea")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(a, 10) + `, "content": "Owes Mika money", "reason": "x"}], "diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Forget(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Insert(ctx, memory.NewMemory{Kind: "fact", Content: "Owes Mika fifty dollars"}, nil); !errors.Is(err, memory.ErrForgotten) {
		t.Errorf("insert of the earlier wording: %v", err)
	}
	k, _ := f.mem.Get(ctx, keep)
	content := "owes mika fifty dollars"
	if _, err := f.mem.UpdateIfUnchanged(ctx, keep, k, memory.Patch{Content: &content}, nil); !errors.Is(err, memory.ErrForgotten) {
		t.Errorf("rewriting into the earlier wording: %v", err)
	}
}

func TestUndoingALaterMergeClearsLaterCopies(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("person", "Mika is Rika's sister")
	src := f.remember("fact", "Mika works nights")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "content": "Mika is Rika's sister and works nights",
		"merged_from": [` + strconv.FormatInt(src, 10) + `], "reason": "merge"}], "diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(24 * time.Hour)
	f.say(f.now.Add(-time.Hour), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "importance": 5, "reason": "matters"}], "diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	days, _ := f.svc.Journal(ctx, 0)
	later, merge := days[0].Reflections[0].Changes[0], days[1].Reflections[0].Changes[0]
	for _, c := range []Change{later, merge} {
		if _, err := f.svc.Undo(ctx, mustID(t, c.ID)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.mem.Forget(ctx, src); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM reflection_changes WHERE before LIKE '%nights%' OR after LIKE '%nights%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("copies of the forgotten words left: %d %v", n, err)
	}
}

func TestUndoWorksForARememberedAgainMemory(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	old := f.remember("fact", "Owes Mika fifty dollars")
	if _, err := f.mem.Forget(ctx, old); err != nil {
		t.Fatal(err)
	}
	again := f.remember("fact", "Owes Mika fifty dollars") // the owner asked her to remember it again
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(again, 10) + `, "importance": 4, "reason": "x"}], "diary": "d"}`
	if summary, err := f.svc.Reflect(ctx); err != nil || !strings.Contains(summary, "updated 1") {
		t.Fatalf("an importance change to a remembered-again memory: %q %v", summary, err)
	}
	days, _ := f.svc.Journal(ctx, 0)
	if _, err := f.svc.Undo(ctx, mustID(t, days[0].Reflections[0].Changes[0].ID)); err != nil {
		t.Errorf("undo: %v", err)
	}
}

func TestAMergeChecksItsSourcesAsTheyAreNow(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	dest := f.remember("person", "Mika is Rika's sister")
	src := f.remember("fact", "Mika works nights")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	model := &editingModel{fakeModel: &fakeModel{reply: `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `,
		"content": "Mika is Rika's sister and works nights", "merged_from": [` + strconv.FormatInt(src, 10) + `], "reason": "merge"}],
		"retire": [{"id": ` + strconv.FormatInt(src, 10) + `, "reason": "merged"}], "diary": "d"}`}}
	model.during = func() {
		days := "Mika works days"
		if _, err := f.mem.Update(ctx, src, memory.Patch{Content: &days}); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.llm = model
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.mem.Get(ctx, dest); m.Content != "Mika is Rika's sister" {
		t.Errorf("merged an out-of-date source: %+v", m)
	}
	if m, err := f.mem.Get(ctx, src); err != nil || m.Content != "Mika works days" {
		t.Errorf("source = %+v %v", m, err)
	}
}

func TestAChainedMergeIsUndoneFromTheEnd(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a := f.remember("fact", "A fact")
	b := f.remember("fact", "B fact")
	c := f.remember("fact", "C fact")
	merge := func(dest, src int64, content string) {
		f.say(f.now.Add(-time.Hour), "hi", "hello")
		f.model.reply = `{"update": [{"id": ` + strconv.FormatInt(dest, 10) + `, "content": "` + content + `", "merged_from": [` + strconv.FormatInt(src, 10) + `], "reason": "merge"}], "diary": "d"}`
		if _, err := f.svc.Reflect(ctx); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(24 * time.Hour)
	}
	merge(a, b, "A fact and B fact")
	merge(c, a, "C fact, A fact and B fact")
	days, _ := f.svc.Journal(ctx, 0)
	first := days[1].Reflections[0].Changes[0]
	var he *httpx.Error
	if _, err := f.svc.Undo(ctx, mustID(t, first.ID)); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("undoing the earlier merge first: %v", err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, days[0].Reflections[0].Changes[0].ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, first.ID)); err != nil {
		t.Errorf("undoing from the end: %v", err)
	}
}
