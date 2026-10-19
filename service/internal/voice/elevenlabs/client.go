// Package elevenlabs implements voice.Synthesizer on the ElevenLabs
// text-to-speech API, using the with-timestamps endpoint so every character
// (including [expression] tags, which eleven_v3/v4 perform) has a start and end.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/voice"
)

const (
	defaultBaseURL = "https://api.elevenlabs.io"
	outputFormat   = "mp3_44100_128"

	defaultTimeout = 20 * time.Second // per HTTP attempt
	maxRetries     = 2
	baseBackoff    = 400 * time.Millisecond
	maxRetryAfter  = 10 * time.Second // a longer Retry-After isn't worth blocking a spoken sentence for
	maxBody        = 32 << 20
)

var _ voice.Synthesizer = (*Client)(nil)

// Client talks to one voice with one model. It is safe for concurrent use.
type Client struct {
	apiKey  string
	voiceID string
	model   string

	http    *http.Client
	baseURL string
	timeout time.Duration
	log     *slog.Logger
	sleep   func(context.Context, time.Duration) error // replaced in tests
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client (its Timeout is left alone; the
// per-attempt timeout is applied through the request context).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithBaseURL points the client somewhere else, e.g. an httptest server.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithLogger sets the logger. The API key and the text are never logged.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithTimeout sets the per-attempt timeout (default 20 s).
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// New returns a client. model is e.g. "eleven_v4" or "eleven_v4_turbo" (understand [expression]
// tags) or "eleven_flash_v2_5" (faster, no tags).
func New(apiKey, voiceID, model string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		voiceID: voiceID,
		model:   model,
		http:    &http.Client{},
		baseURL: defaultBaseURL,
		timeout: defaultTimeout,
		log:     slog.Default(),
		sleep:   sleepCtx,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

type timestampsResponse struct {
	AudioBase64 string `json:"audio_base64"`
	Alignment   *struct {
		Characters []string  `json:"characters"`
		Starts     []float64 `json:"character_start_times_seconds"`
		Ends       []float64 `json:"character_end_times_seconds"`
	} `json:"alignment"`
}

// Synthesize speaks text (which may contain [expression] tags) and returns mp3
// audio with per-character timing for exactly that text.
func (c *Client) Synthesize(ctx context.Context, text string) (*voice.Speech, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("elevenlabs: %w: empty text", ErrBadRequest)
	}
	if c.apiKey == "" || c.voiceID == "" {
		return nil, fmt.Errorf("elevenlabs: %w: missing API key or voice id", ErrAuth)
	}
	body, err := json.Marshal(map[string]string{"text": text, "model_id": c.model})
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: encode request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/v1/text-to-speech/%s/with-timestamps?output_format=%s",
		c.baseURL, url.PathEscape(c.voiceID), outputFormat)

	start := time.Now()
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		raw, reqID, err := c.attempt(ctx, endpoint, body)
		if err == nil {
			sp, perr := parse(raw)
			if perr != nil {
				return nil, fmt.Errorf("elevenlabs: request %s: %w", reqID, perr)
			}
			c.log.Debug("elevenlabs synthesized", "chars", len(text), "request_id", reqID,
				"audio_bytes", len(sp.Audio), "duration_s", sp.Duration, "took", time.Since(start).Round(time.Millisecond))
			return sp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, fmt.Errorf("elevenlabs: %w", ctx.Err())
		}
		wait, retry := c.retryPlan(err, attempt)
		if !retry || attempt == maxRetries {
			break
		}
		c.log.Warn("elevenlabs request failed, retrying", "chars", len(text), "request_id", reqID,
			"attempt", attempt+1, "wait", wait, "err", err)
		if serr := c.sleep(ctx, wait); serr != nil {
			return nil, fmt.Errorf("elevenlabs: %w", serr)
		}
	}
	return nil, lastErr
}

// attempt performs one HTTP call and returns the body of a 2xx answer.
func (c *Client) attempt(ctx context.Context, endpoint string, body []byte) (raw []byte, requestID string, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body)) //nolint:gosec // G704: the base URL is operator config (or a test server), not request input
	if err != nil {
		return nil, "", fmt.Errorf("elevenlabs: build request: %w", err)
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req) //nolint:gosec // G704: see NewRequestWithContext above
	if err != nil {
		// url.Error repeats the URL (no key, no text), so wrapping it is safe.
		return nil, "", fmt.Errorf("elevenlabs: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	requestID = resp.Header.Get("request-id")
	if requestID == "" {
		requestID = resp.Header.Get("x-request-id")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, requestID, fmt.Errorf("elevenlabs: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		e := classify(resp.StatusCode, data)
		e.RequestID = requestID
		if secs, perr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); perr == nil && secs > 0 {
			e.RetryAfter = secs
		}
		return nil, requestID, e
	}
	return data, requestID, nil
}

// retryPlan decides whether err is worth another attempt and how long to wait.
func (c *Client) retryPlan(err error, attempt int) (time.Duration, bool) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		if !apiErr.retryable() {
			return 0, false
		}
		if apiErr.RetryAfter > 0 {
			d := time.Duration(apiErr.RetryAfter) * time.Second
			if d > maxRetryAfter {
				return 0, false
			}
			return d, true
		}
		return backoff(attempt), true
	}
	// Transport failures and truncated bodies are transient; our own build or
	// decode errors never reach here.
	return backoff(attempt), true
}

// backoff is exponential with jitter in [0.5x, 1x] so concurrent sentences
// don't retry in lockstep.
func backoff(attempt int) time.Duration {
	d := baseBackoff << attempt
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1)) //nolint:gosec // jitter, not security
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func parse(raw []byte) (*voice.Speech, error) {
	var r timestampsResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	audio, err := base64.StdEncoding.DecodeString(r.AudioBase64)
	if err != nil {
		return nil, fmt.Errorf("decode audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, errors.New("response has no audio")
	}
	a := r.Alignment
	if a == nil || len(a.Characters) == 0 {
		return nil, errors.New("response has no alignment")
	}
	if len(a.Starts) != len(a.Characters) || len(a.Ends) != len(a.Characters) {
		return nil, fmt.Errorf("alignment arrays differ in length (%d chars, %d starts, %d ends)",
			len(a.Characters), len(a.Starts), len(a.Ends))
	}
	dur := 0.0
	for _, e := range a.Ends {
		dur = max(dur, e)
	}
	return &voice.Speech{
		Audio:    audio,
		Format:   "mp3",
		Duration: dur,
		Alignment: voice.Alignment{
			Chars:  a.Characters,
			Starts: a.Starts,
			Ends:   a.Ends,
		},
	}, nil
}
