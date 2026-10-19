package elevenlabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const okBody = `{"audio_base64":"` + "SUQzAAAA" + `","alignment":{"characters":["H","i"," ","[","l","]"],` +
	`"character_start_times_seconds":[0,0.1,0.2,0.3,0.3,0.9],"character_end_times_seconds":[0.1,0.2,0.3,0.4,0.9,1.25]}}`

func testClient(t *testing.T, h http.HandlerFunc, opts ...Option) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var sleeps []time.Duration
	opts = append([]Option{WithBaseURL(srv.URL), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, opts...)
	c := New("test-key", "voice/1", "eleven_v3", opts...)
	c.sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	return c, &sleeps
}

func TestSynthesizeSuccess(t *testing.T) {
	var gotReq struct {
		Text    string `json:"text"`
		ModelID string `json:"model_id"`
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/text-to-speech/voice%2F1/with-timestamps" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Get("output_format") != "mp3_44100_128" {
			t.Errorf("output_format = %q", r.URL.Query().Get("output_format"))
		}
		if r.Header.Get("xi-api-key") != "test-key" {
			t.Errorf("missing api key header")
		}
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = io.WriteString(w, okBody)
	})
	sp, err := c.Synthesize(t.Context(), "Hi [l]")
	if err != nil {
		t.Fatal(err)
	}
	if gotReq.Text != "Hi [l]" || gotReq.ModelID != "eleven_v3" {
		t.Errorf("request body = %+v", gotReq)
	}
	wantAudio, _ := base64.StdEncoding.DecodeString("SUQzAAAA")
	if !bytes.Equal(sp.Audio, wantAudio) || sp.Format != "mp3" {
		t.Errorf("audio/format wrong: %q %q", sp.Audio, sp.Format)
	}
	if sp.Duration != 1.25 {
		t.Errorf("duration = %v", sp.Duration)
	}
	if strings.Join(sp.Alignment.Chars, "") != "Hi [l]" || len(sp.Alignment.Starts) != 6 || sp.Alignment.Ends[5] != 1.25 {
		t.Errorf("alignment = %+v", sp.Alignment)
	}
}

func TestSynthesizeRetriesThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c, sleeps := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = io.WriteString(w, okBody)
		}
	})
	if _, err := c.Synthesize(t.Context(), "Hi"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != 2*time.Second {
		t.Fatalf("sleeps = %v (Retry-After not honored)", *sleeps)
	}
}

func TestSynthesizeGivesUpAfterTwoRetries(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("request-id", "req-9")
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.Synthesize(t.Context(), "Hi")
	if !errors.Is(err, ErrServer) {
		t.Fatalf("err = %v, want ErrServer", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.RequestID != "req-9" || apiErr.Status != 500 {
		t.Fatalf("err = %#v", err)
	}
}

func TestSynthesizeErrorKinds(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		calls  int32
	}{
		{"bad key", 401, `{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`, ErrAuth, 1},
		{"quota", 401, `{"detail":{"status":"quota_exceeded","message":"You have 0 credits remaining"}}`, ErrQuota, 1},
		{"payment", 402, ``, ErrQuota, 1},
		{"validation", 422, `{"detail":[{"loc":["body","text"],"msg":"too long","input":"SECRET SENTENCE"}]}`, ErrBadRequest, 1},
		{"rate limit", 429, `{"detail":{"status":"too_many_concurrent_requests","message":"slow down"}}`, ErrRateLimit, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := c.Synthesize(t.Context(), "SECRET SENTENCE")
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error leaks request text: %v", err)
			}
			if calls.Load() != tt.calls {
				t.Errorf("calls = %d, want %d", calls.Load(), tt.calls)
			}
		})
	}
}

func TestSynthesizeRetryAfterTooLongGivesUp(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := c.Synthesize(t.Context(), "Hi"); !errors.Is(err, ErrRateLimit) || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestSynthesizeTimeoutRetriesThenFails(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The server only notices a dropped client once the body has been read.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}, WithTimeout(30*time.Millisecond))
	if _, err := c.Synthesize(t.Context(), "Hi"); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestSynthesizeCallerCancelStops(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.Synthesize(ctx, "Hi"); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestSynthesizeBadResponses(t *testing.T) {
	for name, body := range map[string]string{
		"no alignment":  `{"audio_base64":"SUQzAAAA","alignment":null}`,
		"length differ": `{"audio_base64":"SUQzAAAA","alignment":{"characters":["a","b"],"character_start_times_seconds":[0],"character_end_times_seconds":[1]}}`,
		"no audio":      `{"audio_base64":"","alignment":{"characters":["a"],"character_start_times_seconds":[0],"character_end_times_seconds":[1]}}`,
		"not json":      `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			if _, err := c.Synthesize(t.Context(), "ab"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSynthesizeRejectsEmptyInput(t *testing.T) {
	c := New("k", "v", "m")
	if _, err := c.Synthesize(t.Context(), "  "); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("empty text: %v", err)
	}
	if _, err := New("", "v", "m").Synthesize(t.Context(), "hi"); !errors.Is(err, ErrAuth) {
		t.Fatalf("no key: %v", err)
	}
}

func TestBackoffBounds(t *testing.T) {
	for attempt := range 3 {
		full := baseBackoff << attempt
		for range 100 {
			if d := backoff(attempt); d < full/2 || d > full {
				t.Fatalf("backoff(%d) = %v outside [%v, %v]", attempt, d, full/2, full)
			}
		}
	}
}
