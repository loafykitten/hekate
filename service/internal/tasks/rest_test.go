package tasks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type api struct {
	t   *testing.T
	f   *fixture
	mux *http.ServeMux
}

func newAPI(t *testing.T) *api {
	t.Helper()
	f := newFixture(t)
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	return &api{t: t, f: f, mux: mux}
}

func (a *api) do(method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, r)
	return w
}

func (a *api) task(method, path, body string, status int) Task {
	a.t.Helper()
	w := a.do(method, path, body)
	if w.Code != status {
		a.t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body, status)
	}
	var task Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		a.t.Fatal(err)
	}
	return task
}

func (a *api) wantError(method, path, body string, status int) {
	a.t.Helper()
	w := a.do(method, path, body)
	var e struct {
		Error string `json:"error"`
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Error == "" {
		a.t.Errorf("%s %s = %d %s, want %d with an error message", method, path, w.Code, w.Body, status)
	}
}

func TestRESTLifecycle(t *testing.T) {
	a := newAPI(t)

	created := a.task("POST", "/api/tasks", `{"title":"Oat milk","list":"Groceries","due":"2026-10-20","priority":2}`, 201)
	if created.ID == "" || created.List != "Groceries" || str(created.Due) != "2026-10-20" || created.Priority != 2 {
		t.Fatalf("created = %+v", created)
	}
	if a.f.events.count() != 1 {
		t.Errorf("invalidate after create = %d", a.f.events.count())
	}

	patched := a.task("PATCH", "/api/tasks/"+created.ID, `{"title":"Oat milk (barista)","due":null,"notes":"the blue one"}`, 200)
	if patched.Title != "Oat milk (barista)" || patched.Due != nil || patched.Notes != "the blue one" || patched.List != "Groceries" {
		t.Fatalf("patched = %+v", patched)
	}
	// A client may send back the whole task it fetched.
	whole, _ := json.Marshal(patched)
	a.task("PATCH", "/api/tasks/"+created.ID, string(whole), 200)

	done := a.task("POST", "/api/tasks/"+created.ID+"/complete", "", 200)
	if done.CompletedAt == nil {
		t.Fatalf("complete: %+v", done)
	}
	if w := a.do("GET", "/api/tasks", ""); strings.Contains(w.Body.String(), "Oat milk") {
		t.Errorf("open list still has it: %s", w.Body)
	}
	var listed struct{ Tasks []Task }
	w := a.do("GET", "/api/tasks?status=done&list=groceries", "")
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Tasks) != 1 {
		t.Fatalf("done list: %s", w.Body)
	}
	reopened := a.task("POST", "/api/tasks/"+created.ID+"/reopen", "", 200)
	if reopened.CompletedAt != nil {
		t.Errorf("reopen: %+v", reopened)
	}

	var lists struct{ Lists []ListInfo }
	w = a.do("GET", "/api/lists", "")
	if err := json.Unmarshal(w.Body.Bytes(), &lists); err != nil || len(lists.Lists) != 1 || lists.Lists[0] != (ListInfo{"Groceries", 1}) {
		t.Fatalf("lists: %s", w.Body)
	}

	before := a.f.events.count()
	if w := a.do("DELETE", "/api/tasks/"+created.ID, ""); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if a.f.events.count() != before+1 {
		t.Error("delete did not invalidate")
	}
	a.wantError("DELETE", "/api/tasks/"+created.ID, "", 404)
	a.wantError("POST", "/api/tasks/"+created.ID+"/complete", "", 404)
}

func TestRESTValidation(t *testing.T) {
	a := newAPI(t)
	task := a.task("POST", "/api/tasks", `{"title":"x"}`, 201)
	base := a.f.events.count()

	a.wantError("POST", "/api/tasks", `{"title":""}`, 400)
	a.wantError("POST", "/api/tasks", `{"title":"x","bogus":1}`, 400)
	a.wantError("POST", "/api/tasks", `{"title":"x","priority":"high"}`, 400)
	a.wantError("POST", "/api/tasks", `{"title":"x","due":"someday"}`, 400)
	a.wantError("POST", "/api/tasks", `{"title":"x","rrule":"FREQ=SECONDLY"}`, 400)
	a.wantError("POST", "/api/tasks", `not json`, 400)
	a.wantError("POST", "/api/tasks", ``, 400)
	a.wantError("PATCH", "/api/tasks/"+task.ID, `{"priority":9}`, 400)
	a.wantError("PATCH", "/api/tasks/abc", `{}`, 400)
	a.wantError("PATCH", "/api/tasks/0", `{}`, 400)
	a.wantError("PATCH", "/api/tasks/424242", `{"title":"y"}`, 404)
	a.wantError("GET", "/api/tasks?status=weird", "", 400)
	if a.f.events.count() != base {
		t.Errorf("failed requests emitted %d invalidate events", a.f.events.count()-base)
	}
}

func TestRESTRepeatingTask(t *testing.T) {
	a := newAPI(t)
	task := a.task("POST", "/api/tasks", `{"title":"Take out the recycling","rrule":"FREQ=WEEKLY;BYDAY=MO"}`, 201)
	if str(task.Due) != "2026-10-19" || str(task.RRule) != "FREQ=WEEKLY;BYDAY=MO" {
		t.Fatalf("created = %+v", task)
	}
	next := a.task("POST", "/api/tasks/"+task.ID+"/complete", "", 200)
	if next.CompletedAt != nil || str(next.Due) != "2026-10-26" {
		t.Errorf("after complete = %+v", next)
	}
}
