// Package httpx holds the small JSON helpers every feature package shares, so
// all REST endpoints decode strictly and fail in the same shape:
// { "error": "message" }.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// MaxBody caps request bodies. Everything the menu sends is a few KB.
const MaxBody = 64 << 10

// Error is an error that knows its HTTP status. Domain code returns these for
// problems the caller can fix (bad input, unknown id) and the same message
// reaches both REST clients and the model, which can act on it.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// BadRequest is a 400 with a formatted message.
func BadRequest(format string, a ...any) *Error {
	return &Error{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

// NotFound is a 404 with a formatted message.
func NotFound(format string, a ...any) *Error {
	return &Error{http.StatusNotFound, fmt.Sprintf(format, a...)}
}

// Conflict is a 409 with a formatted message.
func Conflict(format string, a ...any) *Error {
	return &Error{http.StatusConflict, fmt.Sprintf(format, a...)}
}

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// StatusClientClosed is the non-standard 499 for a request the client gave up on.
const StatusClientClosed = 499

// Fail writes err as { "error": … }. An *Error keeps its status and message;
// a request its client cancelled is logged at info and gets a bare 499; anything
// else is logged as an error and reported as an opaque 500.
func Fail(w http.ResponseWriter, log *slog.Logger, r *http.Request, err error) {
	var he *Error
	if errors.As(err, &he) {
		JSON(w, he.Status, map[string]string{"error": he.Msg})
		return
	}
	// The client went away (a closed tab, an aborted fetch): nothing failed
	// on our side, and nobody is left to read a body.
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		if log != nil {
			log.Info("request cancelled by the client", "method", r.Method, "path", r.URL.Path)
		}
		w.WriteHeader(StatusClientClosed)
		return
	}
	if log != nil {
		log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	JSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// NoContent writes a bare 204.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// Decode reads one JSON object into v, rejecting unknown fields, trailing data
// and bodies over MaxBody. An empty body is an error; use DecodeOptional where
// the body may be left out.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	return decode(w, r, v, false)
}

// DecodeOptional is Decode, but an empty body leaves v untouched.
func DecodeOptional(w http.ResponseWriter, r *http.Request, v any) error {
	return decode(w, r, v, true)
}

func decode(w http.ResponseWriter, r *http.Request, v any, emptyOK bool) error {
	body := http.MaxBytesReader(w, r.Body, MaxBody)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &Error{http.StatusRequestEntityTooLarge, "request body is too large"}
		}
		return BadRequest("cannot read the request body")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if emptyOK {
			return nil
		}
		return BadRequest("a JSON body is required")
	}
	// Only JSON: a form or text/plain body is what a cross-site page can send
	// without a preflight. (Cross-origin protection is the primary guard.)
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return &Error{http.StatusUnsupportedMediaType, "send the body as application/json"}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return BadRequest("invalid JSON: %s", jsonProblem(err))
	}
	if dec.More() {
		return BadRequest("invalid JSON: unexpected data after the object")
	}
	return nil
}

// jsonProblem turns decoder errors into a short message without Go type names
// where it can.
func jsonProblem(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) && ute.Field != "" {
		return fmt.Sprintf("%q has the wrong type", ute.Field)
	}
	return err.Error()
}

// Opt is a PATCH field that can be absent, null or set, which a plain pointer
// cannot tell apart.
type Opt[T any] struct {
	Set bool
	Val *T // nil when the JSON value was null
}

// UnmarshalJSON implements json.Unmarshaler.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(bytes.TrimSpace(b)) == "null" {
		o.Val = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err //nolint:wrapcheck // the decoder adds the field name
	}
	o.Val = &v
	return nil
}

// Ptr maps the three states to a pointer: nil when absent, a pointer to the
// zero value when null (so "clear it" is expressible), else the value.
func (o Opt[T]) Ptr() *T {
	if !o.Set {
		return nil
	}
	if o.Val == nil {
		var zero T
		return &zero
	}
	return o.Val
}
