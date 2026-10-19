package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

const (
	defaultMaxRetries = 2
	maxRetryAfter     = 10 * time.Second // longer rate-limit waits are surfaced, not slept
	errorBodyLimit    = 64 << 10
)

// Config configures a Client.
type Config struct {
	BaseURL    string // e.g. https://api.neuralwatt.com/v1
	APIKey     string
	HTTPClient *http.Client // optional; must not set a total Timeout (streams are long-lived)
	Logger     *slog.Logger
	MaxRetries int // retries after the first attempt; 0 means the default of 2, negative disables

	// sleep is replaceable in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// Client talks to the chat completions endpoint.
type Client struct {
	url        string
	key        string
	hc         *http.Client
	log        *slog.Logger
	maxRetries int
	sleep      func(ctx context.Context, d time.Duration) error
}

// New builds a Client.
func New(cfg Config) *Client {
	c := &Client{
		url:        strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions",
		key:        cfg.APIKey,
		hc:         cfg.HTTPClient,
		log:        cfg.Logger,
		maxRetries: cfg.MaxRetries,
		sleep:      cfg.sleep,
	}
	if c.hc == nil {
		c.hc = &http.Client{}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	switch {
	case c.maxRetries == 0:
		c.maxRetries = defaultMaxRetries
	case c.maxRetries < 0:
		c.maxRetries = 0
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	return c
}

// Configured reports whether an API key is set.
func (c *Client) Configured() bool { return c != nil && c.key != "" }

// Stream starts a completion. It returns once the provider has sent its first
// event, so failures that happen before any output (and are worth a retry) are
// retried here; after that, errors surface from Recv.
func (c *Client) Stream(ctx context.Context, req Request) (*Stream, error) {
	body, err := json.Marshal(req.wire())
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for attempt := 0; ; attempt++ {
		s, err := c.attempt(ctx, body)
		if err == nil {
			return s, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm request: %w", ctx.Err())
		}
		delay, retry := c.retryDelay(err, attempt)
		if !retry {
			return nil, err
		}
		c.log.Warn("llm request failed, retrying", "attempt", attempt+1, "delay", delay, "error", err)
		if err := c.sleep(ctx, delay); err != nil {
			return nil, fmt.Errorf("llm retry wait: %w", err)
		}
	}
}

func (c *Client) retryDelay(err error, attempt int) (time.Duration, bool) {
	if attempt >= c.maxRetries {
		return 0, false
	}
	var e *Error
	if !errors.As(err, &e) {
		return 0, false
	}
	switch e.Kind {
	case KindTransient:
		return backoff(attempt), true
	case KindRateLimit:
		if e.RetryAfter > maxRetryAfter {
			return 0, false
		}
		return max(e.RetryAfter, backoff(attempt)), true
	case KindAuth, KindBadRequest, KindContextTooLong, KindProtocol:
	}
	return 0, false
}

// backoff is about 400 ms, 800 ms, ... scaled by a random factor in [0.5, 1.0).
func backoff(attempt int) time.Duration {
	base := 400 * time.Millisecond << attempt
	return time.Duration(float64(base) * (0.5 + rand.Float64()/2)) //nolint:gosec // jitter, not security
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // wrapped by the caller
	case <-t.C:
		return nil
	}
}

func (c *Client) attempt(ctx context.Context, body []byte) (*Stream, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	hreq.Header.Set("Authorization", "Bearer "+c.key)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")

	resp, err := c.hc.Do(hreq)
	if err != nil {
		// net/http errors name the URL, never the Authorization header.
		return nil, &Error{Kind: KindTransient, Message: "request failed", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return nil, classifyHTTP(resp.StatusCode, resp.Header, raw)
	}

	s := newStream(resp.Body, resp.Header.Get("X-Request-Id"), resp.Header.Get("X-NW-Service-Tier"))
	// Read the first event now so an error event at the very start is retryable.
	if err := s.step(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
