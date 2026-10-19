package elevenlabs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Kinds of failure a caller can branch on with errors.Is.
var (
	ErrAuth       = errors.New("elevenlabs: authentication failed")
	ErrQuota      = errors.New("elevenlabs: quota exceeded")
	ErrRateLimit  = errors.New("elevenlabs: rate limited")
	ErrBadRequest = errors.New("elevenlabs: bad request")
	ErrServer     = errors.New("elevenlabs: server error")
)

// Error is a non-2xx answer from the API. It never carries the request text:
// ElevenLabs echoes the offending input in some validation errors, so only the
// status, a short message and the request id are kept.
type Error struct {
	Kind       error // one of the Err* values above
	Status     int
	Message    string
	RequestID  string
	RetryAfter int // seconds, from the Retry-After header; 0 if absent
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%v (http %d", e.Kind, e.Status)
	if e.RequestID != "" {
		msg += ", request " + e.RequestID
	}
	msg += ")"
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Kind }

// retryable reports whether trying again could help.
func (e *Error) retryable() bool {
	return errors.Is(e.Kind, ErrRateLimit) || errors.Is(e.Kind, ErrServer)
}

// classify turns a failed response into an *Error. The body is the (already
// size-limited) response; only its detail.status / detail.message are read.
func classify(status int, body []byte) *Error {
	status0, message := parseDetail(body)
	e := &Error{Status: status, Message: message}
	switch {
	case status0 == "quota_exceeded" || status == http.StatusPaymentRequired:
		e.Kind = ErrQuota
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = ErrAuth
	case status == http.StatusTooManyRequests:
		// Concurrency limits arrive as 429 too; both are worth a short retry.
		e.Kind = ErrRateLimit
	case status >= 500:
		e.Kind = ErrServer
	default:
		e.Kind = ErrBadRequest
	}
	return e
}

// parseDetail extracts {"detail":{"status","message"}}, {"detail":"text"} or the
// 422 list form ({"detail":[{"loc","msg"}]}, from which only loc/msg are kept).
func parseDetail(body []byte) (status, message string) {
	var raw struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &raw) != nil || len(raw.Detail) == 0 {
		return "", ""
	}
	var obj struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw.Detail, &obj) == nil && (obj.Status != "" || obj.Message != "") {
		return obj.Status, truncate(obj.Message, 200)
	}
	var text string
	if json.Unmarshal(raw.Detail, &text) == nil {
		return "", truncate(text, 200)
	}
	var list []struct {
		Loc []any  `json:"loc"`
		Msg string `json:"msg"`
	}
	if json.Unmarshal(raw.Detail, &list) == nil && len(list) > 0 {
		parts := make([]string, 0, len(list))
		for _, it := range list {
			parts = append(parts, fmt.Sprintf("%v: %s", it.Loc, it.Msg))
		}
		return "", truncate(strings.Join(parts, "; "), 200)
	}
	return "", ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
