package server

import (
	"net/http"
	"testing"

	"github.com/loafykitten/hekate/service/internal/proto"
)

func TestHubEmitsNotifyAndInvalidate(t *testing.T) {
	env := newWSServer(t, newFakeConv(), nil, false)
	a := env.dial(t)
	readMsg(t, a) // ready
	waitFor(t, func() bool { return env.hub.Clients() == 1 })

	var ev proto.Events = env.hub // the hub is what feature packages are handed
	ev.Invalidate("tasks", "memory")
	m := readMsg(t, a)
	domains, _ := m["domains"].([]any)
	if m["type"] != "invalidate" || len(domains) != 2 || domains[0] != "tasks" || domains[1] != "memory" {
		t.Errorf("invalidate = %v", m)
	}

	card := &proto.Card{Kind: "reminders", ID: "reminders", Title: "Reminder", Data: []byte(`{"items":[]}`)}
	ev.Notify(proto.NewNotify("n1", proto.NotifyKindReminder, "Reminder", "Stretch", card))
	m = readMsg(t, a)
	c, _ := m["card"].(map[string]any)
	if m["type"] != "notify" || m["id"] != "n1" || m["kind"] != "reminder" || m["title"] != "Reminder" || m["body"] != "Stretch" || c["kind"] != "reminders" {
		t.Errorf("notify = %v", m)
	}

	ev.Notify(proto.NewNotify("n2", proto.NotifyKindReminder, "Reminder", "No card", nil))
	if m = readMsg(t, a); m["card"] != nil {
		t.Errorf("card should be omitted: %v", m)
	}
	ev.Invalidate()
	if m = readMsg(t, a); m["domains"] == nil {
		t.Errorf("an empty domain list must be [] not null: %v", m)
	}
}

func TestFeatureRoutesAreRegistered(t *testing.T) {
	var called []string
	h := New(Options{API: []func(*http.ServeMux){
		func(mux *http.ServeMux) {
			mux.HandleFunc("GET /api/widgets", func(w http.ResponseWriter, _ *http.Request) {
				called = append(called, "widgets")
				w.WriteHeader(http.StatusNoContent)
			})
		},
		func(mux *http.ServeMux) {
			mux.HandleFunc("POST /api/gadgets", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
		},
	}}).Routes()

	if rec := do(t, h, "GET", "/api/widgets"); rec.Code != http.StatusNoContent || len(called) != 1 {
		t.Errorf("widgets = %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/gadgets"); rec.Code != http.StatusCreated {
		t.Errorf("gadgets = %d", rec.Code)
	}
	// Unclaimed paths keep the JSON 404.
	if rec := do(t, h, "GET", "/api/nothing"); rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Errorf("unclaimed = %d %q", rec.Code, rec.Body)
	}
}
