package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/voice"
)

// recHub records broadcast events and lets tests wait for one.
type recHub struct {
	mu     sync.Mutex
	events []any
	wake   chan struct{}
}

func newRecHub() *recHub { return &recHub{wake: make(chan struct{}, 1)} }

func (h *recHub) Broadcast(msg any) {
	h.mu.Lock()
	h.events = append(h.events, msg)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *recHub) snapshot() []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]any(nil), h.events...)
}

// waitFor blocks until pred holds for the recorded events.
func (h *recHub) waitFor(t *testing.T, what string, pred func([]any) bool) []any {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if ev := h.snapshot(); pred(ev) {
			return ev
		}
		select {
		case <-h.wake:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; events: %s", what, describe(h.snapshot()))
		}
	}
}

func (h *recHub) waitTurnEnd(t *testing.T, n int) []any {
	return h.waitFor(t, fmt.Sprintf("%d turn_end events", n), func(ev []any) bool { return len(ofType[proto.TurnEnd](ev)) >= n })
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

func describe(events []any) string {
	var parts []string
	for _, e := range events {
		b, _ := json.Marshal(e)
		if len(b) > 160 {
			b = b[:160]
		}
		parts = append(parts, string(b))
	}
	return strings.Join(parts, "\n")
}

// fake LLM

type llmReq struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	Effort    string           `json:"reasoning_effort"`
	Messages  []map[string]any `json:"messages"`
	Tools     []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func (r llmReq) toolNames() []string {
	var out []string
	for _, t := range r.Tools {
		out = append(out, t.Function.Name)
	}
	return out
}

const hang = "<hang>"

type fakeLLM struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []llmReq
	script func(n int, req llmReq) (status int, steps []string)
}

func newFakeLLM(t *testing.T, script func(n int, req llmReq) (int, []string)) *fakeLLM {
	t.Helper()
	f := &fakeLLM{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llmReq
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		n := len(f.reqs)
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		status, steps := script(n, req)
		if status != 0 && status != 200 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"scripted failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-NW-Service-Tier", "standard")
		w.Header().Set("X-Request-Id", fmt.Sprintf("req-%d", n))
		fl := w.(http.Flusher)
		for _, s := range steps {
			if s == hang {
				fl.Flush()
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, "data: "+s+"\n\n")
			fl.Flush()
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) requests() []llmReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llmReq(nil), f.reqs...)
}

func chunk(choice string) string {
	return `{"id":"c","model":"m","choices":[` + choice + `]}`
}

func tx(s string) string {
	b, _ := json.Marshal(s)
	return chunk(`{"index":0,"delta":{"content":` + string(b) + `},"finish_reason":null}`)
}

func call(idx int, id, name, args string) string {
	a, _ := json.Marshal(args)
	return chunk(fmt.Sprintf(`{"index":0,"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]},"finish_reason":null}`, idx, id, name, a))
}

func fin(reason string) string {
	return chunk(`{"index":0,"delta":{},"finish_reason":"` + reason + `"}`)
}

func usageChunk(in, out int) string {
	return fmt.Sprintf(`{"id":"c","choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"prompt_tokens_details":{"cached_tokens":1}}}`, in, out)
}

// say builds a complete text reply, one chunk per piece.
func say(pieces ...string) []string {
	var out []string
	for _, p := range pieces {
		out = append(out, tx(p))
	}
	return append(out, fin("stop"), usageChunk(10, 5), "[DONE]")
}

// sayThenHang streams pieces and then stalls until the request is cancelled.
func sayThenHang(pieces ...string) []string {
	var out []string
	for _, p := range pieces {
		out = append(out, tx(p))
	}
	return append(out, hang)
}

func callTool(prefix, id, name, args string) []string {
	var out []string
	if prefix != "" {
		out = append(out, tx(prefix))
	}
	return append(out, call(0, id, name, args), fin("tool_calls"), usageChunk(20, 8), "[DONE]")
}

// fake synthesizer

type fakeSynth struct {
	mu    sync.Mutex
	texts []string
	delay func(text string) time.Duration
	fail  func(text string) bool
}

func (f *fakeSynth) Synthesize(ctx context.Context, text string) (*voice.Speech, error) {
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	if f.delay != nil {
		select {
		case <-time.After(f.delay(text)):
		case <-ctx.Done():
			return nil, fmt.Errorf("fake synth: %w", ctx.Err())
		}
	}
	if f.fail != nil && f.fail(text) {
		return nil, fmt.Errorf("fake synth: scripted failure")
	}
	var a voice.Alignment
	at := 0.0
	for _, r := range text {
		a.Chars = append(a.Chars, string(r))
		a.Starts = append(a.Starts, at)
		at += 0.05
		a.Ends = append(a.Ends, at)
	}
	return &voice.Speech{Audio: []byte("audio:" + text), Format: "mp3", Duration: at, Alignment: a}, nil
}

func (f *fakeSynth) spoken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// environment

type env struct {
	t     *testing.T
	agent *Agent
	hub   *recHub
	llm   *fakeLLM
	db    *store.Store
}

type envOpts struct {
	opener  func(ctx context.Context, userText string) (Opening, bool)
	checkIn func(ctx context.Context, day string) (Opening, bool)
	synth   voice.Synthesizer
	tools   []Tool
	sources []Source
	limits  Limits
	noKey   bool
}

var testChat = config.Model{Name: "chat-model", Effort: "low"}
var testDeep = config.Model{Name: "deep-model", Effort: "high"}

func writeManifest(t *testing.T, gestures ...string) string {
	t.Helper()
	dir := t.TempDir()
	type anim struct {
		Name     string `json:"name"`
		Category string `json:"category"`
	}
	anims := []anim{{"idle", "idle"}, {"talking", "talk"}}
	for _, g := range gestures {
		anims = append(anims, anim{g, "gesture"})
	}
	b, _ := json.Marshal(map[string]any{"animations": anims})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newEnv(t *testing.T, script func(n int, req llmReq) (int, []string), o envOpts) *env {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := newFakeLLM(t, script)
	key := "test-key"
	if o.noKey {
		key = ""
	}
	client := llm.New(llm.Config{BaseURL: f.srv.URL, APIKey: key, MaxRetries: -1})
	reg, err := NewRegistry(o.tools...)
	if err != nil {
		t.Fatal(err)
	}
	h := newRecHub()
	a, err := New(Options{
		LLM: client, Store: db, Hub: h, Synth: o.synth, Tools: reg, Context: o.sources, Tamer: "Rika",
		Location: time.UTC, Chat: testChat, Deep: testDeep, AssetsDir: writeManifest(t, "waving", "thankful"),
		Limits: o.limits, Opener: o.opener, CheckIn: o.checkIn,
		Now: func() time.Time { return time.Date(2026, 10, 19, 14, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Shutdown(sctx)
	})
	return &env{t: t, agent: a, hub: h, llm: f, db: db}
}

func (e *env) messages() []store.Message {
	e.t.Helper()
	m, err := e.db.RecentMessages(e.t.Context(), ConversationID, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) count(query string) int {
	e.t.Helper()
	var n int
	if err := e.db.DB().QueryRowContext(e.t.Context(), query).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}
