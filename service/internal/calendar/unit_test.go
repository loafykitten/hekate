package calendar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUnconfigured(t *testing.T) {
	s := New(Options{})
	if s.Configured() {
		t.Fatal("configured without a URL")
	}
	for _, tl := range s.Tools() {
		res, err := tl.Run(context.Background(), json.RawMessage(`{"uid":"x","title":"t","start":"2026-10-20"}`))
		if err != nil || !strings.Contains(res.Content, "not set up") || res.Card != nil {
			t.Errorf("%s: %+v, %v", tl.Name, res, err)
		}
	}
	if text, err := s.PromptContext(context.Background(), time.Now()); err != nil || text != "" {
		t.Errorf("prompt = %q, %v", text, err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/calendar/status", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"configured":false}` {
		t.Errorf("status = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/calendar/events", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("events = %d %s", rec.Code, rec.Body)
	}
}

func TestToolSchemasAreValid(t *testing.T) {
	for _, tl := range New(Options{}).Tools() {
		var v map[string]any
		if err := json.Unmarshal(tl.Schema, &v); err != nil {
			t.Errorf("%s: %v", tl.Name, err)
		}
		if tl.Description == "" || tl.Label == "" || tl.Run == nil {
			t.Errorf("%s incomplete", tl.Name)
		}
	}
}

// A configured service whose server is unreachable still validates input
// before it ever talks to the server.
func TestRESTValidation(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	s := New(Options{URL: dead.URL + "/", Username: "u", Password: "p", Location: time.UTC})
	mux := http.NewServeMux()
	s.Register(mux)

	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"unknown field", "POST", "/api/calendar/events", `{"title":"a","start":"2026-10-20","bogus":1}`, 400},
		{"trailing data", "POST", "/api/calendar/events", `{"title":"a","start":"2026-10-20"} {}`, 400},
		{"not json", "POST", "/api/calendar/events", `nope`, 400},
		{"no title", "POST", "/api/calendar/events", `{"start":"2026-10-20"}`, 400},
		{"bad start", "POST", "/api/calendar/events", `{"title":"a","start":"soon"}`, 400},
		{"too big", "POST", "/api/calendar/events", `{"title":"` + strings.Repeat("a", 70<<10) + `"}`, 413},
		{"patch unknown field", "PATCH", "/api/calendar/events/x", `{"colour":"red"}`, 400},
		{"bad range", "GET", "/api/calendar/events?from=2026-10-20&to=2026-10-19", "", 400},
		{"huge range", "GET", "/api/calendar/events?from=2020-01-01&to=2026-10-19", "", 400},
		{"bad from", "GET", "/api/calendar/events?from=whenever", "", 400},
		{"server down", "GET", "/api/calendar/events", "", 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
			var m map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m["error"] == "" {
				t.Errorf("body = %s", rec.Body)
			}
			if strings.Contains(rec.Body.String(), "127.0.0.1") {
				t.Errorf("error leaks the server address: %s", rec.Body)
			}
		})
	}
}

func TestResolveRange(t *testing.T) {
	loc := chicago(t)
	now := time.Date(2026, 10, 19, 9, 30, 0, 0, loc)
	s := New(Options{Location: loc, Now: func() time.Time { return now }})
	tests := []struct {
		from, to string
		wantFrom string
		wantTo   string
	}{
		{"", "", "2026-10-19T00:00", "2026-10-27T00:00"},
		{"2026-10-20", "2026-10-20", "2026-10-20T00:00", "2026-10-21T00:00"},
		{"2026-10-20T08:00", "", "2026-10-20T08:00", "2026-10-27T08:00"},
		{"2026-10-20", "2026-10-22T12:00", "2026-10-20T00:00", "2026-10-22T12:00"},
	}
	for _, tt := range tests {
		from, to, err := s.resolveRange(tt.from, tt.to)
		if err != nil {
			t.Fatal(err)
		}
		if from != at(t, loc, tt.wantFrom) || to != at(t, loc, tt.wantTo) {
			t.Errorf("(%q,%q) = %v..%v, want %s..%s", tt.from, tt.to, from, to, tt.wantFrom, tt.wantTo)
		}
	}
}

func TestPromptRendering(t *testing.T) {
	loc := chicago(t)
	s := mustSeries(t, weekly, loc)
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, loc)
	occ, _ := s.occurrences(day, day.AddDate(0, 0, 2), loc)
	svc := New(Options{Location: loc})
	got := svc.render(occ, day)
	want := "Calendar today (Tue Oct 20): 15:00-16:00 Standup, weekly @ Room 4\nCalendar tomorrow (Wed Oct 21): nothing scheduled"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if oneLine("a\n\nb   c", 3) != "a b…" {
		t.Errorf("oneLine = %q", oneLine("a\n\nb   c", 3))
	}
}
