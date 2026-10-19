package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/voice"
)

// fakeConv records what the sockets ask of the conversation.
type fakeConv struct {
	mu        sync.Mutex
	said      []said
	interrupt []int
	msgs      []proto.HistoryMessage
	lastQuery struct {
		before int64
		limit  int
	}
	sayCh chan said
}

type said struct{ origin, id, text string }

func newFakeConv() *fakeConv { return &fakeConv{sayCh: make(chan said, 8)} }

func (f *fakeConv) ConversationID() string { return "default" }
func (f *fakeConv) Gestures() []string     { return []string{"waving"} }
func (f *fakeConv) Say(origin, id, text string) {
	f.mu.Lock()
	f.said = append(f.said, said{origin, id, text})
	f.mu.Unlock()
	f.sayCh <- said{origin, id, text}
}
func (f *fakeConv) Interrupt(h int) {
	f.mu.Lock()
	f.interrupt = append(f.interrupt, h)
	f.mu.Unlock()
}
func (f *fakeConv) Messages(_ context.Context, before int64, limit int) ([]proto.HistoryMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastQuery.before, f.lastQuery.limit = before, limit
	return f.msgs, nil
}

type fakeSTT struct {
	ready bool
	text  string
	err   error
	mu    sync.Mutex
	got   []int16
	rate  int
}

func (f *fakeSTT) Ready() bool { return f.ready }
func (f *fakeSTT) Transcribe(_ context.Context, pcm []int16, rate int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got, f.rate = pcm, rate
	return f.text, f.err
}

type wsEnv struct {
	srv *httptest.Server
	hub *Hub
}

func newWSServer(t *testing.T, conv Conversation, stt voice.Transcriber, tts bool) *wsEnv {
	t.Helper()
	hub := NewHub(nil)
	handler := New(Options{Conversation: conv, Hub: hub, STT: stt, TTS: tts}).Routes()
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { hub.Close(); srv.Close() })
	return &wsEnv{srv: srv, hub: hub}
}

func (e *wsEnv) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/ws", nil)
	closeBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// closeBody closes a handshake response; a successful upgrade has no body.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func readMsg(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("bad json %q: %v", data, err)
	}
	return m
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func readUntil(t *testing.T, c *websocket.Conn, typ string) (all []map[string]any) {
	t.Helper()
	for {
		m := readMsg(t, c)
		all = append(all, m)
		if m["type"] == typ {
			return all
		}
	}
}

func TestReadyMessage(t *testing.T) {
	env := newWSServer(t, newFakeConv(), &fakeSTT{ready: true}, true)
	m := readMsg(t, env.dial(t))
	caps, _ := m["capabilities"].(map[string]any)
	if m["type"] != "ready" || m["conversation_id"] != "default" || !strings.HasPrefix(m["client_id"].(string), "c_") ||
		caps["stt"] != true || caps["tts"] != true {
		t.Fatalf("ready = %v", m)
	}
	if g, _ := m["gestures"].([]any); len(g) != 1 || g[0] != "waving" {
		t.Errorf("gestures = %v", m["gestures"])
	}

	// STT not ready yet, TTS off.
	env2 := newWSServer(t, newFakeConv(), &fakeSTT{ready: false}, false)
	caps2, _ := readMsg(t, env2.dial(t))["capabilities"].(map[string]any)
	if caps2["stt"] != false || caps2["tts"] != false {
		t.Errorf("caps = %v", caps2)
	}
	env3 := newWSServer(t, newFakeConv(), nil, false)
	if caps3, _ := readMsg(t, env3.dial(t))["capabilities"].(map[string]any); caps3["stt"] != false {
		t.Errorf("nil STT caps = %v", caps3)
	}
}

func TestSayInterruptAndValidation(t *testing.T) {
	conv := newFakeConv()
	env := newWSServer(t, conv, nil, false)
	c := env.dial(t)
	ready := readMsg(t, c)

	send(t, c, map[string]any{"type": "say", "id": "u1", "text": "  hello there  "})
	got := <-conv.sayCh
	if got.origin != ready["client_id"] || got.id != "u1" || got.text != "hello there" {
		t.Errorf("said = %+v", got)
	}

	send(t, c, map[string]any{"type": "say", "id": "u2", "text": "   "}) // ignored
	send(t, c, map[string]any{"type": "say", "id": "u3", "text": strings.Repeat("é", 4001)})
	if m := readMsg(t, c); m["type"] != "error" || m["code"] != "too_long" || m["input_id"] != "u3" {
		t.Errorf("too long: %v", m)
	}
	send(t, c, map[string]any{"type": "say", "text": "no id"})
	if m := readMsg(t, c); m["code"] != "bad_message" {
		t.Errorf("no id: %v", m)
	}
	send(t, c, map[string]any{"type": "dance"})
	if m := readMsg(t, c); m["code"] != "bad_message" {
		t.Errorf("unknown type: %v", m)
	}
	if err := c.Write(t.Context(), websocket.MessageText, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if m := readMsg(t, c); m["code"] != "bad_message" {
		t.Errorf("bad json: %v", m)
	}

	send(t, c, map[string]any{"type": "interrupt", "heard_seq": 3})
	send(t, c, map[string]any{"type": "interrupt"})
	send(t, c, map[string]any{"type": "interrupt", "heard_seq": -1})
	waitFor(t, func() bool { conv.mu.Lock(); defer conv.mu.Unlock(); return len(conv.interrupt) == 3 })
	conv.mu.Lock()
	defer conv.mu.Unlock()
	if fmt.Sprint(conv.interrupt) != "[3 -1 -1]" {
		t.Errorf("interrupts = %v", conv.interrupt)
	}
	if len(conv.said) != 1 {
		t.Errorf("said = %+v", conv.said)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met in time")
}

func pcmBytes(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func writeBin(t *testing.T, c *websocket.Conn, b []byte) {
	t.Helper()
	if err := c.Write(t.Context(), websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}

func TestPushToTalk(t *testing.T) {
	conv := newFakeConv()
	stt := &fakeSTT{ready: true, text: " turn on the lights "}
	env := newWSServer(t, conv, stt, false)
	c := env.dial(t)
	ready := readMsg(t, c)

	send(t, c, map[string]any{"type": "ptt_start", "id": "p1", "sample_rate": 16000})
	// 4000 samples in frames that split a sample in two.
	audio := make([]int16, 4000)
	for i := range audio {
		audio[i] = int16(i - 2000)
	}
	raw := pcmBytes(audio...)
	writeBin(t, c, raw[:1001])
	writeBin(t, c, raw[1001:4003])
	writeBin(t, c, raw[4003:])
	send(t, c, map[string]any{"type": "ptt_end", "id": "p1"})

	if m := readMsg(t, c); m["type"] != "transcript" || m["id"] != "p1" || m["text"] != "turn on the lights" {
		t.Fatalf("transcript = %v", m)
	}
	got := <-conv.sayCh
	if got.id != "p1" || got.text != "turn on the lights" || got.origin != ready["client_id"] {
		t.Errorf("said = %+v", got)
	}
	stt.mu.Lock()
	defer stt.mu.Unlock()
	if stt.rate != 16000 || len(stt.got) != 4000 || stt.got[0] != -2000 || stt.got[3999] != 1999 {
		t.Errorf("stt got %d samples at %d Hz", len(stt.got), stt.rate)
	}
}

func TestPushToTalkEdgeCases(t *testing.T) {
	t.Run("empty transcript starts no turn", func(t *testing.T) {
		conv := newFakeConv()
		env := newWSServer(t, conv, &fakeSTT{ready: true, text: "  "}, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		writeBin(t, c, pcmBytes(make([]int16, 8000)...))
		send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
		if m := readMsg(t, c); m["type"] != "transcript" || m["text"] != "" {
			t.Fatalf("transcript = %v", m)
		}
		select {
		case s := <-conv.sayCh:
			t.Fatalf("turn started: %+v", s)
		case <-time.After(100 * time.Millisecond):
		}
	})
	t.Run("tap too short skips STT", func(t *testing.T) {
		stt := &fakeSTT{ready: true, text: "noise"}
		env := newWSServer(t, newFakeConv(), stt, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		writeBin(t, c, pcmBytes(1, 2, 3))
		send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
		if m := readMsg(t, c); m["type"] != "transcript" || m["text"] != "" {
			t.Fatalf("transcript = %v", m)
		}
		if stt.got != nil {
			t.Error("STT must not run on a mis-tap")
		}
	})
	t.Run("audio is cut at 60 seconds", func(t *testing.T) {
		stt := &fakeSTT{ready: true, text: "long"}
		conv := newFakeConv()
		env := newWSServer(t, conv, stt, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		frame := make([]byte, proto.MaxBinaryFrame)
		for range (70 * 16000 * 2) / len(frame) {
			writeBin(t, c, frame)
		}
		send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
		<-conv.sayCh
		stt.mu.Lock()
		defer stt.mu.Unlock()
		if len(stt.got) != 60*16000 {
			t.Errorf("samples = %d, want %d", len(stt.got), 60*16000)
		}
	})
	t.Run("cancel discards", func(t *testing.T) {
		stt := &fakeSTT{ready: true, text: "x"}
		conv := newFakeConv()
		env := newWSServer(t, conv, stt, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		writeBin(t, c, pcmBytes(make([]int16, 8000)...))
		send(t, c, map[string]any{"type": "ptt_cancel", "id": "p"})
		send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
		send(t, c, map[string]any{"type": "say", "id": "s", "text": "after"}) // proves the socket is still processing
		if got := <-conv.sayCh; got.text != "after" {
			t.Errorf("said = %+v", got)
		}
		if stt.got != nil {
			t.Error("cancelled audio was transcribed")
		}
	})
	t.Run("no STT", func(t *testing.T) {
		env := newWSServer(t, newFakeConv(), nil, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		if m := readMsg(t, c); m["code"] != "stt_unavailable" || m["input_id"] != "p" {
			t.Fatalf("error = %v", m)
		}
	})
	t.Run("STT stopped being ready", func(t *testing.T) {
		stt := &fakeSTT{ready: true, err: voice.ErrNotReady}
		env := newWSServer(t, newFakeConv(), stt, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		writeBin(t, c, pcmBytes(make([]int16, 8000)...))
		send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
		if m := readMsg(t, c); m["code"] != "stt_unavailable" {
			t.Fatalf("error = %v", m)
		}
	})
	t.Run("STT failure", func(t *testing.T) {
		stt := &fakeSTT{ready: true, err: errors.New("model exploded")}
		env := newWSServer(t, newFakeConv(), stt, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
		writeBin(t, c, pcmBytes(make([]int16, 8000)...))
		send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
		m := readMsg(t, c)
		if m["code"] != "stt_failed" || strings.Contains(fmt.Sprint(m), "exploded") {
			t.Fatalf("error = %v (must not leak internals)", m)
		}
	})
	t.Run("wrong sample rate", func(t *testing.T) {
		env := newWSServer(t, newFakeConv(), &fakeSTT{ready: true}, false)
		c := env.dial(t)
		readMsg(t, c)
		send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 44100})
		if m := readMsg(t, c); m["code"] != "bad_message" {
			t.Fatalf("error = %v", m)
		}
	})
}

func TestFrameLimits(t *testing.T) {
	env := newWSServer(t, newFakeConv(), nil, false)
	c := env.dial(t)
	readMsg(t, c)
	big := `{"type":"say","id":"x","text":"` + strings.Repeat("a", proto.MaxTextFrameBytes) + `"}`
	if err := c.Write(t.Context(), websocket.MessageText, []byte(big)); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.Read(t.Context())
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Errorf("close status = %v (err %v)", websocket.CloseStatus(err), err)
	}

	c2 := env.dial(t)
	readMsg(t, c2)
	if err := c2.Write(t.Context(), websocket.MessageBinary, make([]byte, proto.MaxBinaryFrame+1)); err != nil {
		t.Fatal(err)
	}
	_, _, err = c2.Read(t.Context())
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Errorf("binary close status = %v (err %v)", websocket.CloseStatus(err), err)
	}
}

func TestCrossOriginRejected(t *testing.T) {
	env := newWSServer(t, newFakeConv(), nil, false)
	u := "ws" + strings.TrimPrefix(env.srv.URL, "http") + "/api/ws"
	_, resp, err := websocket.Dial(t.Context(), u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	defer closeBody(resp)
	if err == nil {
		t.Fatal("cross-origin upgrade was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("response = %+v", resp)
	}
	// Same origin is fine.
	c, resp2, err := websocket.Dial(t.Context(), u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {env.srv.URL}}})
	closeBody(resp2)
	if err != nil {
		t.Fatalf("same-origin dial: %v", err)
	}
	_ = c.CloseNow()
}

func TestBroadcastReachesEveryClientAndSlowOnesAreDropped(t *testing.T) {
	env := newWSServer(t, newFakeConv(), nil, false)
	a, b := env.dial(t), env.dial(t)
	readMsg(t, a)
	readMsg(t, b)
	waitFor(t, func() bool { return env.hub.Clients() == 2 })
	env.hub.Broadcast(proto.NewActivity("t1", "x", "Working", "start"))
	for _, c := range []*websocket.Conn{a, b} {
		if m := readMsg(t, c); m["type"] != "activity" || m["turn_id"] != "t1" {
			t.Errorf("event = %v", m)
		}
	}

	// A client that never drains: its queue fills and it is kicked, and Broadcast never blocks.
	kicked := make(chan struct{})
	slow := &client{id: "slow", send: make(chan []byte, 2), kick: func() { close(kicked) }}
	if !env.hub.add(slow) {
		t.Fatal("add failed")
	}
	done := make(chan struct{})
	go func() {
		for range 10 {
			env.hub.Broadcast(proto.NewTranscript("i", "text"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Broadcast blocked on a slow client")
	}
	select {
	case <-kicked:
	default:
		t.Error("slow client was not dropped")
	}
	env.hub.Close()
	if env.hub.add(&client{id: "late", send: make(chan []byte, 1), kick: func() {}}) {
		t.Error("a closed hub must refuse clients")
	}
}

func TestMessagesEndpoint(t *testing.T) {
	conv := newFakeConv()
	conv.msgs = []proto.HistoryMessage{{ID: "4", Role: "user", Text: "hi", CreatedAt: "2026-10-19T09:00:00Z"}, {ID: "5", Role: "assistant", Text: "There you are.", CreatedAt: "2026-10-19T09:00:02Z"}}
	h := New(Options{Conversation: conv, Hub: NewHub(nil)}).Routes()

	get := func(q string) (int, map[string]any) {
		rec := do(t, h, "GET", "/api/conversation/messages"+q)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	code, body := get("?before=9&limit=2")
	msgs, _ := body["messages"].([]any)
	if code != 200 || len(msgs) != 2 || msgs[1].(map[string]any)["text"] != "There you are." || msgs[0].(map[string]any)["id"] != "4" {
		t.Fatalf("%d %v", code, body)
	}
	if conv.lastQuery.before != 9 || conv.lastQuery.limit != 2 {
		t.Errorf("query = %+v", conv.lastQuery)
	}
	get("")
	if conv.lastQuery.before != 0 || conv.lastQuery.limit != 50 {
		t.Errorf("defaults = %+v", conv.lastQuery)
	}
	for _, bad := range []string{"?before=abc", "?before=-1", "?limit=0", "?limit=101", "?limit=x"} {
		if code, body := get(bad); code != 400 || body["error"] == nil {
			t.Errorf("%s: %d %v", bad, code, body)
		}
	}
	conv.msgs = nil
	if _, body := get(""); fmt.Sprint(body["messages"]) != "[]" {
		t.Errorf("empty history = %v", body["messages"])
	}
	// Without a conversation the endpoint is a JSON 404, never the SPA.
	if rec := do(t, New(Options{}).Routes(), "GET", "/api/conversation/messages"); rec.Code != 404 {
		t.Errorf("unwired endpoint = %d", rec.Code)
	}
}

// End to end: a real agent, a scripted model and a real socket.

func fakeModel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"{waving} Morning, Rika. "},"finish_reason":null}]}`,
			`{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"[softly] Did you sleep?"},"finish_reason":null}]}`,
			`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(w, "data: "+ev+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConversationOverWebSocket(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "manifest.json"),
		[]byte(`{"animations":[{"name":"idle","category":"idle"},{"name":"waving","category":"gesture"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	model := fakeModel(t)
	hub := NewHub(nil)
	ag, err := agent.New(agent.Options{
		LLM:   llm.New(llm.Config{BaseURL: model.URL, APIKey: "k", MaxRetries: -1}),
		Store: db, Hub: hub, Tamer: "Rika", AssetsDir: assets,
		Chat: config.Model{Name: "chat", Effort: "low"}, Deep: config.Model{Name: "deep", Effort: "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ag.Shutdown(c)
	})
	srv := httptest.NewServer(New(Options{Conversation: ag, Hub: hub, DB: db}).Routes())
	t.Cleanup(func() { hub.Close(); srv.Close() })
	env := &wsEnv{srv: srv, hub: hub}

	phone, desktop := env.dial(t), env.dial(t)
	ready := readMsg(t, phone)
	if ready["type"] != "ready" || ready["conversation_id"] != "default" {
		t.Fatalf("ready = %v", ready)
	}
	if g, _ := ready["gestures"].([]any); len(g) != 1 || g[0] != "waving" {
		t.Errorf("gestures = %v", ready["gestures"])
	}
	readMsg(t, desktop)
	waitFor(t, func() bool { return hub.Clients() == 2 })

	send(t, phone, map[string]any{"type": "say", "id": "u1", "text": "hey, I just woke up"})
	for name, c := range map[string]*websocket.Conn{"phone": phone, "desktop": desktop} {
		msgs := readUntil(t, c, "turn_end")
		var types []string
		for _, m := range msgs {
			types = append(types, m["type"].(string))
		}
		if strings.Join(types, ",") != "turn_start,segment,segment,turn_end" {
			t.Fatalf("%s got %v", name, types)
		}
		start, seg0, seg1, end := msgs[0], msgs[1], msgs[2], msgs[3]
		if start["user_text"] != "hey, I just woke up" || start["input_id"] != "u1" || start["origin"] != ready["client_id"] {
			t.Errorf("%s turn_start = %v", name, start)
		}
		if seg0["turn_id"] != start["turn_id"] || seg0["seq"] != 0.0 || seg0["text"] != "Morning, Rika." ||
			seg0["audio_b64"] != nil || seg0["words"] != nil {
			t.Errorf("%s segment 0 = %v", name, seg0)
		}
		if g := seg0["gestures"].([]any); len(g) != 1 || g[0].(map[string]any)["name"] != "waving" {
			t.Errorf("%s gestures = %v", name, seg0["gestures"])
		}
		if seg1["seq"] != 1.0 || seg1["text"] != "Did you sleep?" || len(seg1["expressions"].([]any)) != 1 {
			t.Errorf("%s segment 1 = %v", name, seg1)
		}
		usage := end["usage"].(map[string]any)
		if end["status"] != "done" || end["text"] != "Morning, Rika. Did you sleep?" || end["model"] != "chat" ||
			usage["input_tokens"] != 12.0 || usage["output_tokens"] != 7.0 || usage["cost_usd"] != nil {
			t.Errorf("%s turn_end = %v", name, end)
		}
	}

	// History over REST.
	resp, err := http.Get(srv.URL + "/api/conversation/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Messages []proto.HistoryMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "user" || body.Messages[1].Text != "Morning, Rika. Did you sleep?" {
		t.Errorf("history = %+v", body.Messages)
	}
}

func TestPingsKeepLiveClientsAndDropDeadOnes(t *testing.T) {
	hub := NewHub(nil)
	srv := httptest.NewServer(New(Options{Conversation: newFakeConv(), Hub: hub,
		PingInterval: 30 * time.Millisecond, PingTimeout: 100 * time.Millisecond}).Routes())
	t.Cleanup(func() { hub.Close(); srv.Close() })
	env := &wsEnv{srv: srv, hub: hub}

	// A client that keeps reading answers pings automatically and stays connected.
	live := env.dial(t)
	readMsg(t, live)
	go func() { _, _, _ = live.Read(context.Background()) }()
	// A client that never reads cannot answer, so the server drops it.
	dead := env.dial(t)
	_ = dead
	waitFor(t, func() bool { return hub.Clients() == 2 })
	waitFor(t, func() bool { return hub.Clients() == 1 })
	time.Sleep(300 * time.Millisecond)
	if hub.Clients() != 1 {
		t.Errorf("clients = %d, the live one must survive", hub.Clients())
	}
}

// loadingSTT becomes ready later, like the real model downloading in the background.
type loadingSTT struct {
	fakeSTT
	ready atomic.Bool
	done  chan struct{}
}

func (l *loadingSTT) Ready() bool           { return l.ready.Load() }
func (l *loadingSTT) Done() <-chan struct{} { return l.done }
func (l *loadingSTT) finish()               { l.ready.Store(true); close(l.done) }
func (l *loadingSTT) Transcribe(ctx context.Context, pcm []int16, rate int) (string, error) {
	return l.fakeSTT.Transcribe(ctx, pcm, rate)
}

// A client that connected while speech-to-text was loading learns when it's ready.
func TestCapabilitiesAnnouncedWhenSTTBecomesReady(t *testing.T) {
	stt := &loadingSTT{done: make(chan struct{})}
	env := newWSServer(t, newFakeConv(), stt, true)
	c := env.dial(t)
	ready := readMsg(t, c)
	if caps := ready["capabilities"].(map[string]any); caps["stt"] != false || caps["tts"] != true {
		t.Fatalf("ready capabilities = %v", caps)
	}
	stt.finish()
	m := readMsg(t, c)
	if m["type"] != "capabilities" {
		t.Fatalf("got %v, want capabilities", m)
	}
	if caps := m["capabilities"].(map[string]any); caps["stt"] != true || caps["tts"] != true {
		t.Errorf("capabilities = %v", caps)
	}
}

// blockingSTT holds each transcription until released.
type blockingSTT struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingSTT) Ready() bool { return true }
func (b *blockingSTT) Transcribe(context.Context, []int16, int) (string, error) {
	b.started <- struct{}{}
	<-b.release
	return "the older spoken request", nil
}

// Typing while an earlier push-to-talk is still being transcribed wins: the
// late transcript must not start a turn that interrupts the newer reply.
func TestStaleTranscriptionIsDropped(t *testing.T) {
	stt := &blockingSTT{started: make(chan struct{}, 1), release: make(chan struct{})}
	conv := newFakeConv()
	env := newWSServer(t, conv, stt, false)
	c := env.dial(t)
	readMsg(t, c)

	send(t, c, map[string]any{"type": "ptt_start", "id": "p", "sample_rate": 16000})
	writeBin(t, c, pcmBytes(make([]int16, 8000)...))
	send(t, c, map[string]any{"type": "ptt_end", "id": "p"})
	<-stt.started
	send(t, c, map[string]any{"type": "say", "id": "s", "text": "newer typed request"})
	if got := <-conv.sayCh; got.text != "newer typed request" {
		t.Fatalf("said = %+v", got)
	}
	close(stt.release)

	select {
	case got := <-conv.sayCh:
		t.Fatalf("stale transcription started a turn: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}
