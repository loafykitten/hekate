package reminders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/loafykitten/hekate/service/internal/agent"
)

func (f *fixture) do(method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, req)
	return w
}

func (f *fixture) reminder(t *testing.T, method, path, body string, status int) Reminder {
	t.Helper()
	w := f.do(method, path, body)
	if w.Code != status {
		t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body, status)
	}
	var r Reminder
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixture) wantError(t *testing.T, method, path, body string, status int) {
	t.Helper()
	w := f.do(method, path, body)
	var e struct {
		Error string `json:"error"`
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Error == "" {
		t.Errorf("%s %s = %d %s, want %d with an error message", method, path, w.Code, w.Body, status)
	}
}

func TestRESTLifecycle(t *testing.T) {
	f := newFixture(t)
	r := f.reminder(t, "POST", "/api/reminders", `{"text":"Call the dentist","at":"2026-10-20T09:00"}`, 201)
	if r.ID == "" || r.At != "2026-10-20T09:00:00-05:00" || r.Status != "scheduled" {
		t.Fatalf("created = %+v", r)
	}
	if f.events.invalidations() != 1 {
		t.Errorf("invalidations after create = %d", f.events.invalidations())
	}

	r = f.reminder(t, "PATCH", "/api/reminders/"+r.ID, `{"text":"Call the dentist back","rrule":"FREQ=WEEKLY;BYDAY=TU"}`, 200)
	if r.Text != "Call the dentist back" || str(r.RRule) != "FREQ=WEEKLY;BYDAY=TU" {
		t.Fatalf("patched = %+v", r)
	}
	r = f.reminder(t, "PATCH", "/api/reminders/"+r.ID, `{"rrule":null}`, 200)
	if r.RRule != nil {
		t.Errorf("rrule not cleared: %+v", r)
	}

	r = f.reminder(t, "POST", "/api/reminders/"+r.ID+"/snooze", `{"minutes":45}`, 200)
	if r.At != "2026-10-19T14:45:00-05:00" {
		t.Errorf("snoozed = %+v", r)
	}

	var list struct{ Reminders []Reminder }
	if err := json.Unmarshal(f.do("GET", "/api/reminders", "").Body.Bytes(), &list); err != nil || len(list.Reminders) != 1 {
		t.Fatalf("upcoming: %v %+v", err, list)
	}

	r = f.reminder(t, "POST", "/api/reminders/"+r.ID+"/done", "", 200)
	if r.Status != "done" {
		t.Errorf("done = %+v", r)
	}
	if err := json.Unmarshal(f.do("GET", "/api/reminders?status=past", "").Body.Bytes(), &list); err != nil || len(list.Reminders) != 1 {
		t.Fatalf("past: %v %+v", err, list)
	}

	before := f.events.invalidations()
	if w := f.do("DELETE", "/api/reminders/"+r.ID, ""); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if f.events.invalidations() != before+1 {
		t.Error("cancel did not invalidate")
	}
	if got := f.get(t, r.ID); got.Status != "cancelled" {
		t.Errorf("after delete = %+v", got)
	}
}

func TestRESTValidation(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, "x", "2026-10-20T09:00", "")
	before := f.events.invalidations()

	f.wantError(t, "POST", "/api/reminders", `{"text":"x","at":"2026-10-18T09:00"}`, 400) // past
	f.wantError(t, "POST", "/api/reminders", `{"text":"x","at":"2026-10-20"}`, 400)       // no time of day
	f.wantError(t, "POST", "/api/reminders", `{"text":"","at":"2026-10-20T09:00"}`, 400)
	f.wantError(t, "POST", "/api/reminders", `{"text":"x"}`, 400)
	f.wantError(t, "POST", "/api/reminders", `{"text":"x","at":"2026-10-20T09:00","minutes":5}`, 400)
	f.wantError(t, "POST", "/api/reminders", `{"text":"x","at":"2026-10-20T09:00","rrule":"FREQ=MINUTELY"}`, 400)
	f.wantError(t, "PATCH", "/api/reminders/"+r.ID, `{"at":"yesterday"}`, 400)
	f.wantError(t, "PATCH", "/api/reminders/abc", `{}`, 400)
	f.wantError(t, "PATCH", "/api/reminders/999", `{"text":"y"}`, 404)
	f.wantError(t, "POST", "/api/reminders/"+r.ID+"/snooze", `{"minutes":0}`, 400)
	f.wantError(t, "POST", "/api/reminders/"+r.ID+"/snooze", `{}`, 400)
	f.wantError(t, "POST", "/api/reminders/999/snooze", `{"minutes":5}`, 404)
	f.wantError(t, "POST", "/api/reminders/999/done", ``, 404)
	f.wantError(t, "DELETE", "/api/reminders/999", ``, 404)
	f.wantError(t, "GET", "/api/reminders?status=weird", ``, 400)
	if f.events.invalidations() != before {
		t.Errorf("failed requests emitted %d invalidate events", f.events.invalidations()-before)
	}
}

func runTool(t *testing.T, f *fixture, name, args string) (agent.Result, error) {
	t.Helper()
	for _, tl := range f.svc.Tools() {
		if tl.Name == name {
			return tl.Run(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %q", name)
	return agent.Result{}, nil
}

func TestTools(t *testing.T) {
	f := newFixture(t)
	if _, err := agent.NewRegistry(f.svc.Tools()...); err != nil {
		t.Fatal(err)
	}

	res, err := runTool(t, f, "reminder_create", `{"text":"Stretch","in_minutes":2}`)
	if err != nil || !strings.Contains(res.Content, `[1] "Stretch" at 2026-10-19T14:02:00-05:00 (scheduled)`) {
		t.Fatalf("create in minutes: %v %q", err, res.Content)
	}
	var card struct {
		Items     []Reminder `json:"items"`
		Highlight []string   `json:"highlight"`
	}
	if res.Card == nil || res.Card.Kind != "reminders" || json.Unmarshal(res.Card.Data, &card) != nil ||
		len(card.Items) != 1 || card.Highlight[0] != "1" {
		t.Errorf("card = %+v / %+v", res.Card, card)
	}

	res, err = runTool(t, f, "reminder_create", `{"text":"Water the plants","at":"2026-10-20T08:00","rrule":"FREQ=DAILY"}`)
	if err != nil || !strings.Contains(res.Content, "repeats FREQ=DAILY") {
		t.Fatalf("create at: %v %q", err, res.Content)
	}

	for _, args := range []string{
		`{"text":"x"}`, `{"text":"x","at":"2026-10-19T09:00"}`, `{"text":"x","at":"2026-10-20T09:00","in_minutes":5}`,
		`{"text":"x","in_minutes":-1}`, `{"at":"2026-10-20T09:00"}`, `{"text":"x","in_minutes":5,"extra":1}`,
	} {
		if _, err := runTool(t, f, "reminder_create", args); err == nil {
			t.Errorf("reminder_create %s accepted", args)
		}
	}
	// "already passed" must say so in words the model can act on.
	_, err = runTool(t, f, "reminder_create", `{"text":"x","at":"2026-10-19T09:00"}`)
	if err == nil || !strings.Contains(err.Error(), "already passed") {
		t.Errorf("past error = %v", err)
	}

	res, err = runTool(t, f, "reminder_list", `{}`)
	if err != nil || strings.Count(res.Content, "\n") != 1 || !strings.Contains(res.Content, "Stretch") {
		t.Errorf("list: %v %q", err, res.Content)
	}

	res, err = runTool(t, f, "reminder_update", `{"id":"1","text":"Stretch your legs","in_minutes":10}`)
	if err != nil || !strings.Contains(res.Content, "14:10:00") || !strings.Contains(res.Content, "Stretch your legs") {
		t.Errorf("update: %v %q", err, res.Content)
	}
	res, err = runTool(t, f, "reminder_cancel", `{"id":1}`)
	if err != nil || !strings.Contains(res.Content, "cancelled") {
		t.Errorf("cancel: %v %q", err, res.Content)
	}
	// The cancelled one is still shown, highlighted, alongside what is upcoming.
	if json.Unmarshal(res.Card.Data, &card) != nil || len(card.Items) != 2 || card.Highlight[0] != "1" {
		t.Errorf("cancel card = %+v", card)
	}
	if _, err := runTool(t, f, "reminder_cancel", `{"id":99}`); err == nil {
		t.Error("cancelled a reminder that does not exist")
	}
	res, _ = runTool(t, f, "reminder_list", `{"status":"past"}`)
	if !strings.Contains(res.Content, "cancelled") {
		t.Errorf("past list = %q", res.Content)
	}
}
