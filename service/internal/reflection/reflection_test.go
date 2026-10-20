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
	keep := f.remember("person", "Mika is Rika's sister")
	f.say(time.Date(2026, 10, 19, 9, 15, 0, 0, f.loc), "The interview got moved to Thursday at 2, ugh.", "Thursday it is. You'll be ready.")
	f.say(time.Date(2026, 10, 19, 21, 40, 0, 0, f.loc), "Mika's coming over Saturday. Also I'm off coffee now.", "Noted. I'll ask how it went with Mika.")
	f.say(time.Date(2026, 10, 18, 20, 0, 0, 0, f.loc), "too old to read", "and this") // before the window

	f.model.reply = "Here you go:\n```json\n" + `{
		"save": [
			{"kind": "preference", "content": "Has given up coffee", "importance": 3, "reason": "said so tonight"},
			{"kind": "follow_up", "content": "Ask how Mika's visit went", "follow_up_at": "2026-10-25", "reason": "visit Saturday"},
			{"kind": "wish", "content": "bad kind", "reason": "x"}
		],
		"update": [
			{"id": "` + strconv.FormatInt(old, 10) + `", "content": "Has a job interview on Thursday at 2 pm", "kind": "follow_up", "follow_up_at": "2026-10-23T17:00", "reason": "moved"},
			{"id": ` + strconv.FormatInt(dupB, 10) + `, "content": "Likes oolong tea, most mornings", "reason": "merged with a duplicate"},
			{"id": 9999, "content": "not a memory", "reason": "x"},
			{"id": ` + strconv.FormatInt(keep, 10) + `, "reason": "no fields"}
		],
		"retire": [{"id": ` + strconv.FormatInt(dupA, 10) + `, "reason": "merged into the other"}],
		"diary": "Rika's interview moved. They sounded tired, but steadier than last week.",
		"summary": ""
	}` + "\n```"
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Saved 2, updated 2 and retired 1 memories; wrote the diary for 2026-10-19 (3 changes refused)" {
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
	// An update whose memory was forgotten since can't be undone.
	if _, err := f.mem.Forget(ctx, dupB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Undo(ctx, mustID(t, ch[1].ID)); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("undoing an update of a forgotten memory: %v", err)
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
	if err != nil || !strings.Contains(out, "Your diary, 2026-10-19:\nA gentle day.") || !strings.Contains(out, `saved "Had lunch with Mika" (nice day)`) {
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
