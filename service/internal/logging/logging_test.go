package logging

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func rec(level slog.Level, msg string, at time.Time) slog.Record {
	return slog.NewRecord(at, level, msg, 0)
}

func TestRingOrderAndWrap(t *testing.T) {
	r := NewRing(3)
	h := newRingHandler(r)
	base := time.Now()
	for i := range 5 {
		_ = h.Handle(context.Background(), rec(slog.LevelInfo, fmt.Sprint("m", i), base.Add(time.Duration(i)*time.Second)))
	}
	got := r.Query(Query{})
	if len(got) != 3 || got[0].Message != "m4" || got[1].Message != "m3" || got[2].Message != "m2" {
		t.Fatalf("got %+v", got)
	}
	if got := r.Query(Query{Limit: 1}); len(got) != 1 || got[0].Message != "m4" {
		t.Fatalf("limit: %+v", got)
	}
}

func TestRingFilters(t *testing.T) {
	r := NewRing(10)
	l := slog.New(newRingHandler(r))
	l.Debug("noise")
	l.Info("hello world", "user", "Alice")
	l.Warn("disk low")
	time.Sleep(5 * time.Millisecond)
	cut := time.Now()
	time.Sleep(5 * time.Millisecond)
	l.Error("boom", slog.Group("req", slog.String("id", "abc123")))

	tests := []struct {
		name string
		q    Query
		want []string
	}{
		{"all", Query{MinLevel: slog.LevelDebug}, []string{"boom", "disk low", "hello world", "noise"}},
		{"min level", Query{MinLevel: slog.LevelWarn}, []string{"boom", "disk low"}},
		{"since", Query{Since: cut}, []string{"boom"}},
		{"message substring", Query{Substring: "WORLD"}, []string{"hello world"}},
		{"attr substring", Query{Substring: "alice"}, []string{"hello world"}},
		{"group attr", Query{Substring: "req.id=abc"}, []string{"boom"}},
		{"no match", Query{Substring: "zzz"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, rc := range r.Query(tt.q) {
				got = append(got, rc.Message)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing(100)
	l := slog.New(newRingHandler(r)).With("k", "v").WithGroup("g")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 200 {
				l.Info("msg", "i", i, "j", j)
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				_ = r.Query(Query{Substring: "msg", Limit: 10})
			}
		}()
	}
	wg.Wait()
	if got := r.Query(Query{}); len(got) != 100 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestMultiPropagatesAttrsAndGroups(t *testing.T) {
	var a, b bytes.Buffer
	ring := NewRing(10)
	m := NewMulti(slog.NewJSONHandler(&a, nil), slog.NewJSONHandler(&b, nil), newRingHandler(ring))
	l := slog.New(m).With("svc", "x").WithGroup("req").With("id", 7)
	l.Info("hi", "path", "/p")

	for name, out := range map[string]string{"a": a.String(), "b": b.String()} {
		if !strings.Contains(out, `"svc":"x"`) || !strings.Contains(out, `"req":{"id":7,"path":"/p"}`) {
			t.Errorf("%s output missing attrs/groups: %s", name, out)
		}
	}
	got := ring.Query(Query{})
	if len(got) != 1 || got[0].Attrs["svc"] != "x" || got[0].Attrs["req.id"] != "7" || got[0].Attrs["req.path"] != "/p" {
		t.Fatalf("ring: %+v", got)
	}
}

func TestNewWritesFileAndRing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	lg, err := New(dir, slog.LevelInfo, &out)
	if err != nil {
		t.Fatal(err)
	}
	lg.Debug("hidden")
	lg.Info("visible")
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "logs", "hekate.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "visible") || strings.Contains(string(data), "hidden") {
		t.Errorf("file: %s", data)
	}
	if !strings.Contains(out.String(), "visible") {
		t.Errorf("stdout: %s", out.String())
	}
	if got := lg.Ring.Query(Query{}); len(got) != 1 || got[0].Message != "visible" {
		t.Errorf("ring: %+v", got)
	}
}
