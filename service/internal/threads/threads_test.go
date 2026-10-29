package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

// fake model: each request takes the next scripted reply; "hang" stalls until cancelled.

type modelReq struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	Tools    []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

type fakeModel struct {
	mu     sync.Mutex
	reqs   []modelReq
	script func(req modelReq) []string
}

func (f *fakeModel) requests() []modelReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modelReq(nil), f.reqs...)
}

func chunk(delta string) string {
	return `{"id":"c","model":"m","choices":[{"index":0,"delta":` + delta + `}]}`
}

func reply(text string) []string {
	b, _ := json.Marshal(text)
	return []string{chunk(`{"content":` + string(b) + `}`), `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}`, "[DONE]"}
}

func callTool(name, args string) []string {
	a, _ := json.Marshal(args)
	return []string{chunk(fmt.Sprintf(`{"tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":%q,"arguments":%s}}]}`, name, a)),
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]"}
}

const hang = "<hang>"

type recHub struct {
	mu     sync.Mutex
	events []any
}

func (h *recHub) Broadcast(msg any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, msg)
}

// inner returns the events wrapped for thread id, unwrapped.
func (h *recHub) inner(id string) []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []any
	for _, e := range h.events {
		if te, ok := e.(proto.ThreadEvent); ok && te.Thread == id {
			out = append(out, te.Event)
		}
	}
	return out
}

func (h *recHub) unwrapped() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.events {
		if _, ok := e.(proto.ThreadEvent); !ok {
			n++
		}
	}
	return n
}

type recEvents struct {
	mu    sync.Mutex
	notes []proto.Notify
}

func (r *recEvents) Invalidate(...string) {}

func (r *recEvents) Notify(n proto.Notify) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
}

func (r *recEvents) notified() []proto.Notify {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.Notify(nil), r.notes...)
}

type recPush struct {
	mu   sync.Mutex
	sent []push.Payload
}

func (p *recPush) Send(_ context.Context, pl push.Payload, _ push.Urgency) (push.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, pl)
	return push.Result{}, nil
}

func (p *recPush) urls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, pl := range p.sent {
		out = append(out, pl.URL)
	}
	return out
}

type fixture struct {
	svc    *Service
	model  *fakeModel
	hub    *recHub
	events *recEvents
	push   *recPush
	db     *store.Store
}

var (
	deep = config.Model{Name: "glm-deep", Effort: "high"}
	chat = config.Model{Name: "glm-flash", Effort: "low"}
)

// newFixture builds the service; with, if given, adjusts its options first.
func newFixture(t *testing.T, with ...func(*Options)) *fixture {
	t.Helper()
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{model: &fakeModel{script: func(modelReq) []string { return reply("Done.") }}, hub: &recHub{},
		events: &recEvents{}, push: &recPush{}, db: db}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req modelReq
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		f.model.mu.Lock()
		f.model.reqs = append(f.model.reqs, req)
		script := f.model.script
		f.model.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, s := range script(req) {
			if s == hang {
				fl.Flush()
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, "data: "+s+"\n\n")
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	echo := agent.Tool{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`), Effect: agent.EffectRead,
		Label: "Echoing", Run: func(context.Context, json.RawMessage) (agent.Result, error) {
			return agent.Result{Content: "echoed"}, nil
		}}
	opts := Options{
		Store: db, LLM: llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k"}), Deep: deep, Chat: chat,
		Tools: []agent.Tool{echo}, System: SystemPrompt("hekate-lab", nil), Hub: f.hub, Events: f.events, Push: f.push,
		Tamer: "Sam", Location: time.UTC,
	}
	for _, w := range with {
		w(&opts)
	}
	f.svc, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := f.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = f.svc.Shutdown(sctx)
	})
	return f
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ofType[T any](events []any) []T {
	var out []T
	for _, e := range events {
		if v, ok := e.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// idle reports whether the thread's last turn has ended, without the busy check
// the code under test uses.
func (s *Service) idle(id string) bool {
	s.mu.Lock()
	c := s.convs[id]
	s.mu.Unlock()
	return c == nil || !c.ag.Busy()
}

func (f *fixture) say(t *testing.T, id, text string) {
	t.Helper()
	in, err := f.svc.Input(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	in.Say("c_1", "in-"+text, text, false)
}

func (f *fixture) turnEnds(id string) []proto.TurnEnd { return ofType[proto.TurnEnd](f.hub.inner(id)) }

func TestCreateListUpdate(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a, err := f.svc.Create(ctx, "", "", OriginOwner)
	if err != nil || a.Title != placeholder || a.Model != ModelDeep || a.ModelName != "glm-deep" || a.Origin != OriginOwner {
		t.Fatalf("created %+v, %v", a, err)
	}
	b, err := f.svc.Create(ctx, "  Arch   research ", ModelChat, OriginRenamon)
	if err != nil || b.Title != "Arch research" || b.ModelName != "glm-flash" {
		t.Fatalf("created %+v, %v", b, err)
	}
	for _, bad := range []struct{ title, model string }{{strings.Repeat("x", 81), ""}, {"", "gpt"}} {
		if _, err := f.svc.Create(ctx, bad.title, bad.model, OriginOwner); err == nil {
			t.Errorf("created with %+v", bad)
		}
	}
	yes, title, model := true, "Renamed", ModelChat
	if a, err = f.svc.Update(ctx, a.ID, Patch{Title: &title, Model: &model, Archived: &yes}); err != nil ||
		a.Title != "Renamed" || a.Model != ModelChat || !a.Archived {
		t.Fatalf("updated %+v, %v", a, err)
	}
	active, _ := f.svc.List(ctx, false)
	archived, _ := f.svc.List(ctx, true)
	if len(active) != 1 || active[0].ID != b.ID || len(archived) != 1 || archived[0].ID != a.ID {
		t.Errorf("active %+v, archived %+v", active, archived)
	}
	if _, err := f.svc.Input(ctx, a.ID); err == nil {
		t.Error("an archived thread took input")
	}
	empty := ""
	if _, err := f.svc.Update(ctx, b.ID, Patch{Title: &empty}); err == nil {
		t.Error("renamed to nothing")
	}
}

func TestThreadTurn(t *testing.T) {
	f := newFixture(t)
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "Find out which Arch ISO mirrors are fastest from the US Midwest, and why that matters")
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })

	if end := f.turnEnds(th.ID)[0]; end.Status != proto.StatusDone || end.Text != "Done." {
		t.Errorf("turn end %+v", end)
	}
	if n := f.hub.unwrapped(); n != 0 {
		t.Errorf("%d thread events went out unwrapped", n)
	}
	got, _ := f.svc.Get(t.Context(), th.ID)
	if got.Title != "Find out which Arch ISO mirrors are fastest from the US…" || got.Preview != "Done." || got.Running {
		t.Errorf("thread after the turn %+v", got)
	}
	// Stored in the thread, not the main conversation.
	main, _ := f.db.VisibleMessages(t.Context(), agent.ConversationID, 0, 10)
	msgs, _, _, _ := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if len(main) != 0 || len(msgs) != 2 || msgs[1].Text != "Done." {
		t.Errorf("main %d messages, thread %+v", len(main), msgs)
	}
	req := f.model.requests()[0]
	system, _ := req.Messages[0]["content"].(string)
	var tools []string
	for _, tl := range req.Tools {
		tools = append(tools, tl.Function.Name)
	}
	if req.Model != "glm-deep" || !strings.Contains(system, "## Where you are") || !strings.Contains(system, "http://hekate-lab:<port>") ||
		strings.Contains(system, "## Performing") || !strings.Contains(system, "Sam") || !slices.Equal(tools, []string{"echo"}) {
		t.Errorf("model %s, tools %v, system prompt:\n%s", req.Model, tools, system)
	}
	// An owner's turn that ended quickly needs no notification.
	if n := f.events.notified(); len(n) != 0 {
		t.Errorf("notified %+v", n)
	}
}

func TestBusyAndCapacity(t *testing.T) {
	f := newFixture(t)
	f.model.script = func(modelReq) []string { return []string{hang} }
	var ids []string
	for i := range 4 {
		th, _ := f.svc.Create(t.Context(), fmt.Sprintf("t%d", i), "", OriginOwner)
		ids = append(ids, th.ID)
	}
	for _, id := range ids[:3] {
		f.say(t, id, "work")
	}
	running := func(id string) bool { r, _ := f.svc.running(id); return r }
	waitFor(t, "three running", func() bool { return running(ids[0]) && running(ids[1]) && running(ids[2]) })

	f.say(t, ids[0], "more")
	f.say(t, ids[3], "work")
	for i, want := range map[int]string{0: stillWorking, 3: tooMany} {
		errs := ofType[proto.Error](f.hub.inner(ids[i]))
		if len(errs) != 1 || errs[0].Code != proto.CodeBusy || errs[0].Message != want {
			t.Errorf("thread %d errors %+v", i, errs)
		}
	}
	if running(ids[3]) {
		t.Error("a fourth thread started")
	}
	// Stopping one makes room.
	in, _ := f.svc.Input(t.Context(), ids[1])
	in.Interrupt("", -1)
	waitFor(t, "thread 1 stopped", func() bool { return !running(ids[1]) })
	f.say(t, ids[3], "work")
	waitFor(t, "thread 3 running", func() bool { return running(ids[3]) })
}

func TestStepsAndModelSwitch(t *testing.T) {
	f := newFixture(t)
	f.model.script = func(req modelReq) []string {
		if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1]["role"] == "tool" {
			return reply("Echo said echoed.")
		}
		return callTool("echo", `{}`)
	}
	th, _ := f.svc.Create(t.Context(), "Echo", ModelChat, OriginOwner)
	f.say(t, th.ID, "echo please")
	// turn_end goes out just before the turn lets go: wait for both, or "again" finds it busy.
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 && f.svc.idle(th.ID) })
	msgs, steps, _, err := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if err != nil || len(msgs) != 2 || len(steps) != 1 || steps[0].Tool != "echo" || steps[0].Label != "Echoing" ||
		steps[0].Status != "ok" || steps[0].Summary != "echoed" || steps[0].TurnID != msgs[1].TurnID {
		t.Fatalf("messages %+v, steps %+v, %v", msgs, steps, err)
	}
	// On Flash, think_deeper is offered; switched to GLM 5.3, it isn't.
	first := f.model.requests()[0]
	if first.Model != "glm-flash" || len(first.Tools) != 2 {
		t.Errorf("flash request: %s with %d tools", first.Model, len(first.Tools))
	}
	model := ModelDeep
	if _, err := f.svc.Update(t.Context(), th.ID, Patch{Model: &model}); err != nil {
		t.Fatal(err)
	}
	f.model.script = func(modelReq) []string { return reply("ok") }
	f.say(t, th.ID, "again")
	waitFor(t, "second turn", func() bool { return len(f.turnEnds(th.ID)) == 2 })
	reqs := f.model.requests()
	if last := reqs[len(reqs)-1]; last.Model != "glm-deep" || len(last.Tools) != 1 {
		t.Errorf("deep request: %s with %d tools", last.Model, len(last.Tools))
	}
}

func run(t *testing.T, tools []agent.Tool, name, args string) (string, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			res, err := tl.Run(t.Context(), json.RawMessage(args))
			return res.Content, err
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

func TestRenamonsTools(t *testing.T) {
	f := newFixture(t)
	f.model.script = func(modelReq) []string { return reply("qBittorrent runs at http://hekate-lab:8080.") }
	tools := f.svc.Tools()
	out, err := run(t, tools, "thread_start", `{"title":"Set up qBittorrent","message":"Set up qBittorrent in the lab."}`)
	if err != nil || !strings.Contains(out, "working on it") {
		t.Fatalf("%q, %v", out, err)
	}
	list, _ := f.svc.List(t.Context(), false)
	if len(list) != 1 || list[0].Origin != OriginRenamon || list[0].Title != "Set up qBittorrent" {
		t.Fatalf("threads %+v", list)
	}
	id := list[0].ID
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(id)) == 1 })
	// She started it, so the owner hears when it's done, and a tap opens it.
	waitFor(t, "notification", func() bool { return len(f.events.notified()) == 1 })
	if n := f.events.notified()[0]; n.Kind != "thread" || n.ID != id || !strings.Contains(n.Body, "8080") {
		t.Errorf("notify %+v", n)
	}
	waitFor(t, "push", func() bool { return len(f.push.urls()) == 1 })
	if got := f.push.urls(); got[0] != "/?thread="+id {
		t.Errorf("push urls %v", got)
	}

	out, err = run(t, tools, "thread_read", `{"thread":"qbit"}`)
	if err != nil || !strings.Contains(out, "Owner: Set up qBittorrent in the lab.") || !strings.Contains(out, "You: qBittorrent runs at") ||
		!strings.Contains(out, "<thread_messages") {
		t.Errorf("read %q, %v", out, err)
	}
	ctxText, _ := f.svc.PromptContext(t.Context(), time.Now())
	if !strings.Contains(ctxText, id) || !strings.Contains(ctxText, "Set up qBittorrent") || !strings.Contains(ctxText, "idle") {
		t.Errorf("context %q", ctxText)
	}
	if out, _ := run(t, tools, "thread_stop", `{"thread":"`+id+`"}`); !strings.Contains(out, "isn't working") {
		t.Errorf("stop %q", out)
	}

	if _, err := f.svc.Create(t.Context(), "Set up Jellyfin", "", OriginOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, tools, "thread_send", `{"thread":"set up","message":"x"}`); err == nil || !strings.Contains(err.Error(), "2 threads match") {
		t.Errorf("ambiguous send: %v", err)
	}
	if _, err := run(t, tools, "thread_read", `{"thread":"nothing like it"}`); err == nil {
		t.Error("read a thread that doesn't exist")
	}
	out, err = run(t, tools, "thread_send", `{"thread":"jellyfin","message":"Use port 8096."}`)
	if err != nil || !strings.Contains(out, "working on it") {
		t.Errorf("send %q, %v", out, err)
	}
}

func TestStartAbortsStaleTurns(t *testing.T) {
	f := newFixture(t)
	th, _ := f.svc.Create(t.Context(), "Old", "", OriginOwner)
	if err := f.db.BeginTurn(t.Context(), store.Turn{ID: "t_stale", ConversationID: th.ID, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.db.DB().QueryRow(`SELECT status FROM turns WHERE id = 't_stale'`).Scan(&status); err != nil || status != store.TurnError {
		t.Errorf("stale turn %q, %v", status, err)
	}
}

func TestSystemPromptWithoutLab(t *testing.T) {
	p := SystemPrompt("", nil)("Sam")
	if strings.Contains(p, "## Your lab") || !strings.Contains(p, "## Who you are") || strings.Contains(p, "{{") {
		t.Errorf("prompt without a lab:\n%s", p)
	}
}

// Her written replies come through exactly as she wrote them: none of the
// voice's tag parsing, sentence joining or markdown stripping.
func TestWrittenRepliesArePreserved(t *testing.T) {
	f := newFixture(t)
	text := "## Mirrors\n\nSee [the list](https://archlinux.org/mirrorlist/) and run:\n\n```sh\ncurl -s 'https://x/?country=US&use_mirror_status=on' | sed 's/^#Server/Server/'\n```\n\nConfig: {\"max_ratio\": 1.5, \"dl_path\": \"/srv/iso\"} [done]\n"
	f.model.script = func(modelReq) []string {
		// Streamed in awkward pieces, as a model does.
		var out []string
		for i := 0; i < len(text); i += 7 {
			b, _ := json.Marshal(text[i:min(i+7, len(text))])
			out = append(out, chunk(`{"content":`+string(b)+`}`))
		}
		return append(out, `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "[DONE]")
	}
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "find mirrors")
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })
	want := text
	if got := f.turnEnds(th.ID)[0].Text; got != want {
		t.Errorf("turn_end text\n got %q\nwant %q", got, want)
	}
	msgs, _, _, _ := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if got := msgs[1].Text; got != want {
		t.Errorf("stored text\n got %q\nwant %q", got, want)
	}
	var streamed strings.Builder
	for _, s := range ofType[proto.Segment](f.hub.inner(th.ID)) {
		streamed.WriteString(s.Text)
	}
	if streamed.String() != text {
		t.Errorf("segments joined\n got %q\nwant %q", streamed.String(), text)
	}
}

// Stopping or archiving after she answered keeps the answer; stopping mid-turn
// keeps what she'd written so far.
func TestStopKeepsWhatWasWritten(t *testing.T) {
	f := newFixture(t)
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "go")
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })
	in, _ := f.svc.Input(t.Context(), th.ID)
	in.Interrupt("", -1)
	yes := true
	if _, err := f.svc.Update(t.Context(), th.ID, Patch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	msgs, _, _, _ := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if len(msgs) != 2 || msgs[1].Text != "Done." || len(f.turnEnds(th.ID)) != 1 {
		t.Errorf("after stop and archive: %+v, %d turn ends", msgs, len(f.turnEnds(th.ID)))
	}

	th2, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.model.script = func(modelReq) []string { return append(reply("First part.\n")[:1], hang) }
	f.say(t, th2.ID, "long job")
	waitFor(t, "first part", func() bool { return len(ofType[proto.Segment](f.hub.inner(th2.ID))) == 1 })
	in2, _ := f.svc.Input(t.Context(), th2.ID)
	in2.Interrupt("", -1)
	waitFor(t, "stopped", func() bool { return len(f.turnEnds(th2.ID)) == 1 })
	if end := f.turnEnds(th2.ID)[0]; end.Status != proto.StatusInterrupted || end.Text != "First part.\n" {
		t.Errorf("stopped turn %+v", end)
	}
}

// A turn she started ends (and notifies) while stops arrive: nothing hangs.
func TestEndWhileStopping(t *testing.T) {
	f := newFixture(t)
	for i := range 20 {
		th, _ := f.svc.Create(t.Context(), fmt.Sprintf("t%d", i), "", OriginRenamon)
		c, err := f.svc.conv(t.Context(), th.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, busy := c.say(OriginRenamon, "r", "go", nil); busy != "" {
			t.Fatal(busy)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for !f.svc.idle(th.ID) {
				c.Interrupt("", -1)
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a turn ending while being stopped hung")
		}
	}
}

// A reply that opens with an indented code block keeps its indentation.
func TestWrittenReplyKeepsLeadingIndentation(t *testing.T) {
	f := newFixture(t)
	text := "    docker compose up -d\n\nThat starts it."
	f.model.script = func(modelReq) []string { return reply(text) }
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "how do I start it")
	waitFor(t, "turn end", func() bool { return len(f.turnEnds(th.ID)) == 1 })
	msgs, _, _, _ := f.svc.Messages(t.Context(), th.ID, 0, 10)
	if got := f.turnEnds(th.ID)[0].Text; got != text || msgs[1].Text != text {
		t.Errorf("turn_end %q, stored %q, want %q", got, msgs[1].Text, text)
	}
}
