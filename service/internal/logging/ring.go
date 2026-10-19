package logging

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Record is a captured log entry with its attributes already rendered.
type Record struct {
	Time    time.Time         `json:"time"`
	Level   slog.Level        `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Query filters ring buffer contents. The zero MinLevel is Info, so pass
// slog.LevelDebug to include debug records.
type Query struct {
	MinLevel  slog.Level
	Since     time.Time
	Substring string // case-insensitive, matched against message and attributes
	Limit     int    // <= 0 means no limit
}

// Ring keeps the last N records in memory. It is shared by every handler
// derived from the one that owns it.
type Ring struct {
	mu   sync.Mutex
	buf  []Record
	next int
	full bool
}

// NewRing returns a ring holding at most size records.
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{buf: make([]Record, size)}
}

func (r *Ring) add(rec Record) {
	r.mu.Lock()
	r.buf[r.next] = rec
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
	r.mu.Unlock()
}

// Query returns matching records, newest first.
func (r *Ring) Query(q Query) []Record {
	r.mu.Lock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	// Snapshot oldest-to-newest so filtering happens outside the lock.
	snap := make([]Record, 0, n)
	if r.full {
		snap = append(snap, r.buf[r.next:]...)
	}
	snap = append(snap, r.buf[:r.next]...)
	r.mu.Unlock()

	needle := strings.ToLower(q.Substring)
	var out []Record
	for _, rec := range slices.Backward(snap) {
		if rec.Level < q.MinLevel || (!q.Since.IsZero() && rec.Time.Before(q.Since)) {
			continue
		}
		if needle != "" && !matches(rec, needle) {
			continue
		}
		out = append(out, rec)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out
}

func matches(rec Record, needle string) bool {
	if strings.Contains(strings.ToLower(rec.Message), needle) {
		return true
	}
	for k, v := range rec.Attrs {
		if strings.Contains(strings.ToLower(k+"="+v), needle) {
			return true
		}
	}
	return false
}

// ringHandler feeds a Ring. Groups are flattened into dotted attribute keys.
type ringHandler struct {
	ring   *Ring
	attrs  map[string]string // pre-rendered WithAttrs, already prefixed
	groups []string
}

func newRingHandler(r *Ring) *ringHandler { return &ringHandler{ring: r} }

func (h *ringHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *ringHandler) Handle(_ context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level, Message: r.Message}
	if len(h.attrs) > 0 || r.NumAttrs() > 0 {
		rec.Attrs = make(map[string]string, len(h.attrs)+r.NumAttrs())
		for k, v := range h.attrs {
			rec.Attrs[k] = v
		}
		r.Attrs(func(a slog.Attr) bool {
			flatten(rec.Attrs, h.groups, a)
			return true
		})
	}
	h.ring.add(rec)
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := &ringHandler{ring: h.ring, groups: h.groups, attrs: make(map[string]string, len(h.attrs)+len(attrs))}
	for k, v := range h.attrs {
		c.attrs[k] = v
	}
	for _, a := range attrs {
		flatten(c.attrs, h.groups, a)
	}
	return c
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &ringHandler{ring: h.ring, attrs: h.attrs, groups: append(slices.Clone(h.groups), name)}
}

func flatten(dst map[string]string, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		g := groups
		if a.Key != "" {
			g = append(slices.Clone(groups), a.Key)
		}
		for _, ga := range a.Value.Group() {
			flatten(dst, g, ga)
		}
		return
	}
	key := strings.Join(append(slices.Clone(groups), a.Key), ".")
	dst[key] = a.Value.String()
}
