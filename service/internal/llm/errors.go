package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind classifies a failure so callers can decide what to do about it.
type Kind int

// Error kinds.
const (
	KindTransient      Kind = iota + 1 // network, 5xx, truncated stream: worth retrying
	KindAuth                           // 401/403: the key is wrong; never retried
	KindRateLimit                      // 429
	KindBadRequest                     // 4xx: our request is wrong
	KindContextTooLong                 // the prompt exceeds the model's context window
	KindProtocol                       // the provider spoke nonsense or broke a limit
)

func (k Kind) String() string {
	switch k {
	case KindTransient:
		return "transient"
	case KindAuth:
		return "auth"
	case KindRateLimit:
		return "rate_limit"
	case KindBadRequest:
		return "bad_request"
	case KindContextTooLong:
		return "context_too_long"
	case KindProtocol:
		return "protocol"
	}
	return "unknown"
}

// Error is the error type returned by the client.
type Error struct {
	Kind       Kind
	Status     int           // HTTP status, 0 for stream-level errors
	Message    string        // provider message, truncated
	RetryAfter time.Duration // set for rate limits when the provider said
	Err        error         // underlying cause, if any
}

func (e *Error) Error() string {
	s := "llm: " + e.Kind.String()
	if e.Status != 0 {
		s += " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of err, or 0 when err is not an *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

const maxMessage = 300

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxMessage {
		return s[:maxMessage] + "…"
	}
	return s
}

// providerError is the OpenAI-style error object, tolerant of string forms.
type providerError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func parseProviderError(raw []byte) providerError {
	var wrapped struct {
		Error  json.RawMessage `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return providerError{Message: clip(string(raw))}
	}
	obj := wrapped.Error
	if len(obj) == 0 {
		obj = wrapped.Detail
	}
	if len(obj) == 0 {
		return providerError{Message: clip(string(raw))}
	}
	var s string
	if json.Unmarshal(obj, &s) == nil {
		return providerError{Message: clip(s)}
	}
	var pe providerError
	if json.Unmarshal(obj, &pe) != nil {
		return providerError{Message: clip(string(obj))}
	}
	pe.Message = clip(pe.Message)
	return pe
}

func (p providerError) text() string {
	return strings.ToLower(p.Message + " " + p.Type + " " + fmt.Sprint(p.Code))
}

func looksContextTooLong(t string) bool {
	for _, s := range []string{"context length", "context_length", "context window", "maximum context", "too many tokens", "prompt is too long", "input is too long", "reduce the length"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// classifyHTTP maps a non-200 response to an *Error.
func classifyHTTP(status int, header http.Header, body []byte) *Error {
	pe := parseProviderError(body)
	e := &Error{Status: status, Message: pe.Message}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = KindAuth
	case status == http.StatusTooManyRequests:
		e.Kind = KindRateLimit
		e.RetryAfter = parseRetryAfter(header.Get("Retry-After"), time.Now())
	case status >= 500 || status == http.StatusRequestTimeout:
		e.Kind = KindTransient
	case status == http.StatusRequestEntityTooLarge || looksContextTooLong(pe.text()):
		e.Kind = KindContextTooLong
	default:
		e.Kind = KindBadRequest
	}
	return e
}

// classifyStreamError maps an error event inside a 200 stream.
func classifyStreamError(raw []byte) *Error {
	pe := parseProviderError(raw)
	t := pe.text()
	e := &Error{Message: pe.Message}
	switch {
	case looksContextTooLong(t):
		e.Kind = KindContextTooLong
	case strings.Contains(t, "rate") && strings.Contains(t, "limit"), strings.Contains(t, "429"), strings.Contains(t, "quota"):
		e.Kind = KindRateLimit
	case strings.Contains(t, "auth"), strings.Contains(t, "api key"), strings.Contains(t, "api_key"), strings.Contains(t, "401"), strings.Contains(t, "403"):
		e.Kind = KindAuth
	case strings.Contains(t, "invalid_request"), strings.Contains(t, "bad request"), strings.Contains(t, "400"):
		e.Kind = KindBadRequest
	default:
		// overloaded, server_error, timeouts and anything unknown
		e.Kind = KindTransient
	}
	return e
}

// parseRetryAfter reads delta-seconds or an HTTP date; 0 when absent or invalid.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
