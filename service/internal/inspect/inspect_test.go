package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/logging"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

type recEvents struct {
	mu          sync.Mutex
	invalidated []string
}

func (r *recEvents) Notify(proto.Notify) {}
func (r *recEvents) Invalidate(domains ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, domains...)
}

type fixture struct {
	store  *store.Store
	svc    *Service
	jobs   *jobs.Runner
	logger *logging.Logger
	events *recEvents
	deploy string
}

func newFixture(t *testing.T, checks ...Check) *fixture {
	t.Helper()
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{store: db, logger: logging.NewDiscard(), events: &recEvents{}, deploy: filepath.Join(t.TempDir(), "deploy.log")}
	f.jobs, err = jobs.New(jobs.Options{Store: db})
	if err != nil {
		t.Fatal(err)
	}
	f.svc, err = New(Options{Store: db, Jobs: f.jobs, Ring: f.logger.Ring, DeployLog: f.deploy, Checks: checks,
		Version: "1.2.3 (abc)", Started: time.Now().Add(-3 * time.Hour), Location: time.UTC, Events: f.events})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) run(t *testing.T, name string) string {
	t.Helper()
	for _, tool := range f.svc.Tools() {
		if tool.Name == name {
			res, err := tool.Run(t.Context(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			return res.Content
		}
	}
	t.Fatalf("no tool %s", name)
	return ""
}

func TestStatus(t *testing.T) {
	f := newFixture(t,
		Check{Name: "Language model", Run: func(context.Context) (bool, string) { return true, "" }},
		Check{Name: "Mail", Run: func(context.Context) (bool, string) { return false, "work failing: login\nrefused" }},
	)
	f.jobs.Add(jobs.Job{Name: "reflection", Run: func(context.Context) (string, error) { return "saved 2 memories", nil }})
	f.jobs.Add(jobs.Job{Name: "briefing", Run: func(context.Context) (string, error) { return "", nil }})
	if err := f.jobs.RunNow(t.Context(), "reflection"); err != nil {
		t.Fatal(err)
	}
	f.logger.Warn("mail sweep failed", "account", "work")
	f.logger.Error("cannot record usage")
	if err := os.WriteFile(f.deploy, []byte(`{"ts":"2026-10-20T09:00:00Z","event":"deploy_started","commit":"0123456789abcdef0123"}
{"ts":"2026-10-20T09:01:00Z","event":"deploy_succeeded","commit":"0123456789abcdef0123"}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := f.run(t, "self_status")
	for _, want := range []string{
		"Hekate 1.2.3 (abc)", "- Language model: ok\n", "- Mail: NOT OK, work failing: login refused",
		"- Routine reflection: ok at", "saved 2 memories", "- Routine briefing: hasn't run yet",
		"1 errors, 1 warnings", "deploy_succeeded commit=0123456789ab",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestLogs(t *testing.T) {
	f := newFixture(t)
	f.logger.Info("turn start", "turn_id", "t1")
	f.logger.Warn("mail sweep failed", "account", "work", "error", "timeout")
	f.logger.Error("cannot record usage")
	out, err := f.svc.Logs(LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ERROR cannot record usage") || !strings.Contains(out, "WARN mail sweep failed account=work error=timeout") || strings.Contains(out, "turn start") {
		t.Errorf("warn and up:\n%s", out)
	}
	if strings.Index(out, "cannot record usage") > strings.Index(out, "mail sweep") {
		t.Errorf("newest first:\n%s", out)
	}
	out, _ = f.svc.Logs(LogQuery{Level: "info", Contains: "TURN"})
	if !strings.Contains(out, "turn start turn_id=t1") || strings.Contains(out, "mail") {
		t.Errorf("contains:\n%s", out)
	}
	if out, _ := f.svc.Logs(LogQuery{Level: "error", Contains: "nothing like this"}); !strings.HasPrefix(out, "Nothing at error") {
		t.Errorf("empty: %s", out)
	}
	if _, err := f.svc.Logs(LogQuery{Level: "loud"}); err == nil {
		t.Error("an unknown level should fail")
	}
}

func cost(v float64) *float64 { return &v }

func TestUsageAndToolActivity(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.store.EnsureConversation(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.BeginTurn(ctx, store.Turn{ID: "t1", ConversationID: "default", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []store.Usage{
		{TurnID: "t1", Model: "chat", InputTokens: 100, OutputTokens: 10, CostUSD: cost(0.01)},
		{TurnID: "t1", Model: "chat", InputTokens: 50, OutputTokens: 5},
	} {
		if err := f.store.AddUsage(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.AddBackgroundUsage(ctx, "mail", store.Usage{Model: "bulk", InputTokens: 900, OutputTokens: 90, CostUSD: cost(0.002)}); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Usage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Last 7 day(s)", "- chat: 2 requests, 150 tokens in, 15 out, $0.0100", "- mail: 1 requests, 900 tokens in, 90 out, $0.0020", "Total: $0.0120 (1 requests had no reported cost)"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}

	for _, a := range []store.Activity{
		{TurnID: "t1", Tool: "task_create", Effect: "local", Result: "Added \"oat milk\"", Status: "ok", DurationMS: 12},
		{TurnID: "t1", Tool: "calendar_events", Effect: "read", Result: "calendar unreachable", Status: "error", DurationMS: 5000},
	} {
		if err := f.store.AddActivity(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	out, err = f.svc.Activity(ctx, ActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "calendar_events error (5000 ms): calendar unreachable") || !strings.Contains(out, "task_create ok") {
		t.Errorf("activity:\n%s", out)
	}
	out, _ = f.svc.Activity(ctx, ActivityQuery{Status: "error"})
	if strings.Contains(out, "task_create") {
		t.Errorf("errors only:\n%s", out)
	}
	if _, err := f.svc.Activity(ctx, ActivityQuery{Kind: "dreams"}); err == nil {
		t.Error("an unknown kind should fail")
	}
}

func TestRoutineAndDeployActivity(t *testing.T) {
	f := newFixture(t)
	f.jobs.Add(jobs.Job{Name: "heartbeat", Run: func(context.Context) (string, error) { return "", errors.New("calendar down") }})
	if err := f.jobs.RunNow(t.Context(), "heartbeat"); err != nil {
		t.Fatal(err)
	}
	out, _ := f.svc.Activity(t.Context(), ActivityQuery{Kind: "routines"})
	if !strings.Contains(out, "heartbeat: failed at") || !strings.Contains(out, "(calendar down)") {
		t.Errorf("routines:\n%s", out)
	}

	// A big log: only its end is read, and the partial first line is skipped.
	var b strings.Builder
	old := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	for b.Len() < deployTail+1000 {
		b.WriteString(`{"ts":"` + old + `","event":"deploy_succeeded","commit":"old"}` + "\n")
	}
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	b.WriteString(`{"ts":"` + recent + `","event":"deploy_failed_rolled_back","commit":"abc","tail":"panic: boom"}` + "\n")
	if err := os.WriteFile(f.deploy, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Activity(t.Context(), ActivityQuery{Kind: "deploys"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deploy_failed_rolled_back commit=abc tail=panic: boom") || strings.Contains(out, "commit=old") {
		t.Errorf("deploys in the last day:\n%s", out)
	}
}

func TestIdeas(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	idea, existed, err := f.svc.LogIdea(ctx, "A tool to read package tracking pages", "tool")
	if err != nil || existed || idea.Status != IdeaOpen {
		t.Fatalf("log: %+v %v %v", idea, existed, err)
	}
	again, existed, _ := f.svc.LogIdea(ctx, "a tool to read package tracking pages ", "tool")
	if !existed || again.ID != idea.ID {
		t.Errorf("a repeat should return the open idea: %+v %v", again, existed)
	}
	if _, _, err := f.svc.LogIdea(ctx, "x", "wish"); err == nil {
		t.Error("an unknown kind should fail")
	}
	for i := range 9 {
		if _, _, err := f.svc.LogIdea(ctx, "idea "+string(rune('a'+i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.svc.LogIdea(ctx, "one too many", ""); !errors.Is(err, ErrIdeaCap) {
		t.Errorf("cap: %v", err)
	}

	srv := httptest.NewServer(func() http.Handler {
		mux := http.NewServeMux()
		f.svc.Routes(mux)
		return mux
	}())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, srv.URL+"/api/ideas/"+idea.ID, strings.NewReader(`{"status":"done"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %v %v", res.StatusCode, err)
	}
	_ = res.Body.Close()
	list, _ := f.svc.Ideas(ctx, false)
	if len(list) != 9 {
		t.Errorf("open ideas = %d", len(list))
	}
	all, _ := f.svc.Ideas(ctx, true)
	if len(all) != 10 || all[len(all)-1].Status != IdeaDone {
		t.Errorf("all = %+v", all)
	}
	res, err = http.Get(srv.URL + "/api/ideas?status=all")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("list: %v", err)
	}
	var body struct{ Ideas []Idea }
	_ = json.NewDecoder(res.Body).Decode(&body)
	_ = res.Body.Close()
	if len(body.Ideas) != 10 {
		t.Errorf("listed %d", len(body.Ideas))
	}
	if err := f.svc.DeleteIdea(ctx, 9999); err == nil {
		t.Error("deleting a missing idea should fail")
	}
	f.events.mu.Lock()
	defer f.events.mu.Unlock()
	if len(f.events.invalidated) < 11 || f.events.invalidated[0] != DomainIdeas {
		t.Errorf("invalidated = %v", f.events.invalidated)
	}
}
