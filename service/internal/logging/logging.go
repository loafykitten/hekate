// Package logging builds the service logger: JSON to stdout, a rotating
// file, and an in-memory ring the agent can query later.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// RingSize is how many recent records are kept in memory.
const RingSize = 2000

// Logger bundles the slog logger with its ring buffer and file closer.
type Logger struct {
	*slog.Logger
	Ring *Ring
	file io.Closer
}

// New builds a logger writing JSON to stdout, <dataDir>/logs/hekate.log and the ring.
func New(dataDir string, level slog.Level, stdout io.Writer) (*Logger, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	file := &lumberjack.Logger{
		Filename:   filepath.Join(dir, "hekate.log"),
		MaxSize:    20, // MB
		MaxBackups: 5,
		Compress:   true,
	}
	return build(level, stdout, file, file), nil
}

// NewDiscard returns a logger for tests that still records into a ring.
func NewDiscard() *Logger { return build(slog.LevelDebug, io.Discard, nil, nil) }

func build(level slog.Level, stdout io.Writer, file io.Writer, closer io.Closer) *Logger {
	opts := &slog.HandlerOptions{Level: level}
	ring := NewRing(RingSize)
	handlers := []slog.Handler{slog.NewJSONHandler(stdout, opts)}
	if file != nil {
		handlers = append(handlers, slog.NewJSONHandler(file, opts))
	}
	// The ring is not level-gated by the handlers' options on purpose: it sits
	// behind the same threshold via the wrapper so debug noise stays out unless asked.
	handlers = append(handlers, &levelHandler{min: level, next: newRingHandler(ring)})
	return &Logger{Logger: slog.New(NewMulti(handlers...)), Ring: ring, file: closer}
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// levelHandler applies a minimum level in front of another handler.
type levelHandler struct {
	min  slog.Level
	next slog.Handler
}

func (h *levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.next.Enabled(ctx, l)
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.next.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &levelHandler{min: h.min, next: h.next.WithAttrs(a)}
}

func (h *levelHandler) WithGroup(n string) slog.Handler {
	return &levelHandler{min: h.min, next: h.next.WithGroup(n)}
}

// Multi fans records out to several handlers.
type Multi struct{ handlers []slog.Handler }

// NewMulti returns a handler that forwards to every child.
func NewMulti(handlers ...slog.Handler) *Multi { return &Multi{handlers: handlers} }

// Enabled reports whether any child wants the level.
func (m *Multi) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

// Handle sends a clone of the record to each interested child and joins errors.
func (m *Multi) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			// Clone: records share attr storage and children may add to it.
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// WithAttrs propagates to every child.
func (m *Multi) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &Multi{handlers: out}
}

// WithGroup propagates to every child.
func (m *Multi) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &Multi{handlers: out}
}
