package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// collect drains a stream.
func collect(t *testing.T, s *Stream) (text string, calls []ToolCall, res *Result, err error) {
	t.Helper()
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return text, calls, res, nil
		}
		if err != nil {
			return text, calls, res, err
		}
		switch ev.Kind {
		case EventText:
			text += ev.Text
		case EventToolCall:
			calls = append(calls, ev.ToolCall)
		case EventDone:
			res = ev.Result
		}
	}
}

func newTestClient(url string) *Client {
	return New(Config{BaseURL: url, APIKey: "test-key", sleep: func(context.Context, time.Duration) error { return nil }})
}

func sseServer(t *testing.T, body string, header http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamOf(t *testing.T, body string) (string, []ToolCall, *Result, error) {
	t.Helper()
	srv := sseServer(t, body, nil)
	s, err := newTestClient(srv.URL).Stream(t.Context(), Request{Model: "m", Effort: "low"})
	if err != nil {
		return "", nil, nil, err
	}
	defer func() { _ = s.Close() }()
	return collect(t, s)
}

func TestRealNeuralwattStream(t *testing.T) {
	srv := sseServer(t, fixture(t, "neuralwatt_basic.sse"), http.Header{
		"X-Nw-Service-Tier": {"standard"},
		"X-Request-Id":      {"req-123"},
	})
	s, err := newTestClient(srv.URL).Stream(t.Context(), Request{Model: "glm-5.3-flash", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	text, calls, res, err := collect(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hi there!" || len(calls) != 0 {
		t.Fatalf("text=%q calls=%v", text, calls)
	}
	if res.FinishReason != "stop" || res.ServiceTier != "standard" || res.RequestID != "req-123" || res.Model != "glm-5.3-flash" {
		t.Errorf("result = %+v", res)
	}
	if res.Usage != (Usage{PromptTokens: 18, CompletionTokens: 5, CachedTokens: 0}) {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.000012 {
		t.Errorf("cost = %v", res.CostUSD)
	}
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("after done: %v", err)
	}
}

func TestTierAndRequestIDFallBackToChunks(t *testing.T) {
	_, _, res, err := streamOf(t, fixture(t, "neuralwatt_basic.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ServiceTier != "standard" || res.RequestID != "chatcmpl-00000000-0000-4000-8000-000000000001" {
		t.Errorf("result = %+v", res)
	}
}

func TestToolCallAssemblyAndReasoningHidden(t *testing.T) {
	text, calls, res, err := streamOf(t, fixture(t, "toolcall.sse"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Let me think." {
		t.Errorf("text = %q (reasoning must never surface)", text)
	}
	if len(calls) != 2 || calls[0].ID != "call_a" || calls[0].Name != "think_deeper" ||
		calls[0].Arguments != `{"reason": "big decision"}` || calls[1].Name != "other" {
		t.Fatalf("calls = %+v", calls)
	}
	if res.FinishReason != "tool_calls" || res.Usage.CachedTokens != 128 || res.CostUSD != nil {
		t.Errorf("result = %+v", res)
	}
}

func chunkLine(content string) string {
	return fmt.Sprintf(`data: {"id":"x","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, content)
}

const finishLine = `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

func TestSSEFramings(t *testing.T) {
	events := []string{chunkLine("Hel"), chunkLine("lo"), finishLine, "data: [DONE]"}
	tests := []struct {
		name string
		body string
	}{
		{"lf", strings.Join(events, "\n\n") + "\n\n"},
		{"crlf", strings.Join(events, "\r\n\r\n") + "\r\n\r\n"},
		{"cr", strings.Join(events, "\r\r") + "\r\r"},
		{"comments and unknown fields", ": ping\n\nid: 7\nretry: 100\n" + strings.Join(events, "\n: keepalive\n\n") + "\n\n"},
		{"no space after colon", strings.ReplaceAll(strings.Join(events, "\n\n"), "data: ", "data:") + "\n\n"},
		{"unterminated last event", strings.Join(events, "\n\n")},
		{"multi-line data", `data: {"id":"x",` + "\n" + `data: "choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n" +
			strings.Join(events[2:], "\n\n") + "\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _, res, err := streamOf(t, tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(text, "Hel") || res == nil || res.FinishReason != "stop" {
				t.Errorf("text=%q res=%+v", text, res)
			}
		})
	}
}

func TestStreamFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Kind
	}{
		{"no DONE", chunkLine("hi") + "\n\n" + finishLine + "\n\n", KindTransient},
		{"no finish reason", chunkLine("hi") + "\n\ndata: [DONE]\n\n", KindTransient},
		{"truncated mid-stream", chunkLine("hi") + "\n\n" + `data: {"id":"x","choi`, KindProtocol},
		{"malformed json", "data: {not json}\n\n", KindProtocol},
		{"error event", "event: error\ndata: {\"error\":{\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\n", KindTransient},
		{"error object", `data: {"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}` + "\n\n", KindRateLimit},
		{"context error", `data: {"error":{"message":"maximum context length is 1000 tokens","code":"context_length_exceeded"}}` + "\n\n", KindContextTooLong},
		{"nameless tool call", `data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n", KindProtocol},
		{"line too long", "data: " + strings.Repeat("a", maxLineBytes+10) + "\n\n", KindProtocol},
		{"empty body", "", KindTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := streamOf(t, tt.body)
			if got := KindOf(err); got != tt.want {
				t.Fatalf("kind = %v (err %v), want %v", got, err, tt.want)
			}
		})
	}
}

func TestStreamSizeLimit(t *testing.T) {
	line := chunkLine(strings.Repeat("x", 100<<10)) + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 200 { // 20 MiB, well past the cap
			if _, err := io.WriteString(w, line); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	s, err := newTestClient(srv.URL).Stream(t.Context(), Request{Model: "m", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, _, _, err = collect(t, s)
	if KindOf(err) != KindProtocol || !errors.Is(err, errStreamTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestShape(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, chunkLine("ok")+"\n\n"+finishLine+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k"})
	s, err := c.Stream(t.Context(), Request{
		Model: "glm", Effort: "low", MaxTokens: 77,
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "f", Arguments: ""}}},
			{Role: RoleTool, ToolCallID: "c1", Content: "result"},
		},
		Tools: []Tool{{Name: "f", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = collect(t, s)
	if auth != "Bearer k" {
		t.Errorf("auth = %q", auth)
	}
	if got["model"] != "glm" || got["reasoning_effort"] != "low" || got["stream"] != true || got["max_tokens"] != 77.0 {
		t.Errorf("body = %v", got)
	}
	if so, _ := got["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("stream_options = %v", got["stream_options"])
	}
	msgs := got["messages"].([]any)
	asst := msgs[2].(map[string]any)
	if asst["content"] != nil {
		t.Errorf("tool-call-only assistant content = %v, want null", asst["content"])
	}
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if tc["type"] != "function" || fn["name"] != "f" || fn["arguments"] != "{}" {
		t.Errorf("tool_calls = %v", tc)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "c1" {
		t.Errorf("tool message = %v", msgs[3])
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["function"].(map[string]any)["name"] != "f" {
		t.Errorf("tools = %v", got["tools"])
	}
}

func TestHTTPErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		header     http.Header
		want       Kind
		wantCalls  int32
		retryAfter time.Duration
	}{
		{"401", 401, `{"error":{"message":"bad key"}}`, nil, KindAuth, 1, 0},
		{"403", 403, `{"detail":"forbidden"}`, nil, KindAuth, 1, 0},
		{"400", 400, `{"error":{"message":"bad param"}}`, nil, KindBadRequest, 1, 0},
		{"400 context", 400, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`, nil, KindContextTooLong, 1, 0},
		{"413", 413, ``, nil, KindContextTooLong, 1, 0},
		{"500 retried", 500, `oops`, nil, KindTransient, 3, 0},
		{"503 retried", 503, ``, nil, KindTransient, 3, 0},
		{"429 retried", 429, `{"error":{"message":"slow down"}}`, http.Header{"Retry-After": {"2"}}, KindRateLimit, 3, 2 * time.Second},
		{"429 long wait surfaced", 429, ``, http.Header{"Retry-After": {"120"}}, KindRateLimit, 1, 120 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				for k, v := range tt.header {
					w.Header()[k] = v
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			_, err := newTestClient(srv.URL).Stream(t.Context(), Request{Model: "m", Effort: "low"})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.Kind != tt.want || e.Status != tt.status || e.RetryAfter != tt.retryAfter {
				t.Errorf("error = %+v", e)
			}
			if calls.Load() != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls.Load(), tt.wantCalls)
			}
		})
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	var slept atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(502)
		case 2:
			// transient provider error event before any output
			_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n")
		default:
			_, _ = io.WriteString(w, chunkLine("ok")+"\n\n"+finishLine+"\n\ndata: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", sleep: func(context.Context, time.Duration) error { slept.Add(1); return nil }})
	s, err := c.Stream(t.Context(), Request{Model: "m", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, err := collect(t, s)
	if err != nil || text != "ok" || calls.Load() != 3 || slept.Load() != 2 {
		t.Fatalf("text=%q err=%v calls=%d slept=%d", text, err, calls.Load(), slept.Load())
	}
}

func TestNetworkErrorRetriedAndKeyNotLeaked(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused
	var slept atomic.Int32
	c := New(Config{BaseURL: url, APIKey: "super-secret-key", sleep: func(context.Context, time.Duration) error { slept.Add(1); return nil }})
	_, err := c.Stream(t.Context(), Request{Model: "m", Effort: "low"})
	if KindOf(err) != KindTransient || slept.Load() != 2 {
		t.Fatalf("err=%v slept=%d", err, slept.Load())
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("key leaked: %v", err)
	}
}

func TestContextCancelStopsStream(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chunkLine("a")+"\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	s, err := newTestClient(srv.URL).Stream(ctx, Request{Model: "m", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	<-started
	if ev, err := s.Recv(); err != nil || ev.Text != "a" {
		t.Fatalf("first: %+v %v", ev, err)
	}
	cancel()
	_, err = s.Recv()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"":                              0,
		"3":                             3 * time.Second,
		"1.5":                           1500 * time.Millisecond,
		"-4":                            0,
		"junk":                          0,
		"Mon, 19 Oct 2026 12:00:07 GMT": 7 * time.Second,
		"Mon, 19 Oct 2026 11:00:00 GMT": 0,
	}
	for in, want := range tests {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
