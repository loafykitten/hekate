package memory

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

func TestRESTMemories(t *testing.T) {
	f := newFixture(t)
	a := f.save(t, "preference", "Takes coffee black", 3)
	f.save(t, "person", "Sister is called Maya", 4)
	f.save(t, "fact", "Coffee shop on Main Street closes at six", 2)
	base := f.events.count()

	var list struct{ Memories []Memory }
	get := func(path string) []Memory {
		t.Helper()
		w := f.do("GET", path, "")
		if w.Code != 200 {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
		}
		list.Memories = nil
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		return list.Memories
	}
	if got := get("/api/memory"); len(got) != 3 || got[0].Content != "Coffee shop on Main Street closes at six" {
		t.Errorf("newest first: %q", contents(got))
	}
	if got := get("/api/memory?q=coffee"); len(got) != 2 || got[0].ID != a.ID {
		t.Errorf("search: %q", contents(got))
	}
	if got := get("/api/memory?kind=person"); len(got) != 1 {
		t.Errorf("kind: %q", contents(got))
	}
	if got := get("/api/memory?limit=1"); len(got) != 1 {
		t.Errorf("limit: %q", contents(got))
	}
	if body := f.do("GET", "/api/memory?q=nothing-matches", "").Body.String(); !strings.Contains(body, `"memories":[]`) {
		t.Errorf("empty result must be [] not null: %s", body)
	}

	w := f.do("PATCH", "/api/memory/"+a.ID, `{"content":"Takes coffee with oat milk","importance":4,"follow_up_at":"2026-10-25"}`)
	var m Memory
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Content != "Takes coffee with oat milk" || m.Importance != 4 ||
		m.FollowUpAt == nil || *m.FollowUpAt != "2026-10-25T09:00:00-05:00" {
		t.Fatalf("patch = %d %s", w.Code, w.Body)
	}
	w = f.do("PATCH", "/api/memory/"+a.ID, `{"follow_up_at":null}`)
	if json.Unmarshal(w.Body.Bytes(), &m) != nil || m.FollowUpAt != nil {
		t.Errorf("follow_up_at not cleared: %s", w.Body)
	}
	if f.events.count() != base+2 {
		t.Errorf("patches emitted %d events, want 2", f.events.count()-base)
	}

	if w := f.do("DELETE", "/api/memory/"+a.ID, ""); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	if f.events.count() != base+3 {
		t.Error("delete did not invalidate")
	}
	f.wantError(t, "DELETE", "/api/memory/"+a.ID, "", 404)
}

func TestRESTValidation(t *testing.T) {
	f := newFixture(t)
	m := f.save(t, "fact", "x", 3)
	base := f.events.count()
	f.wantError(t, "GET", "/api/memory?limit=0", "", 400)
	f.wantError(t, "GET", "/api/memory?limit=9999", "", 400)
	f.wantError(t, "GET", "/api/memory?limit=abc", "", 400)
	f.wantError(t, "GET", "/api/memory?kind=gossip", "", 400)
	f.wantError(t, "GET", "/api/memory?q=x&kind=gossip", "", 400)
	f.wantError(t, "PATCH", "/api/memory/"+m.ID, `{"importance":9}`, 400)
	f.wantError(t, "PATCH", "/api/memory/"+m.ID, `{"kind":"rumor"}`, 400)
	f.wantError(t, "PATCH", "/api/memory/"+m.ID, `{"content":""}`, 400)
	f.wantError(t, "PATCH", "/api/memory/"+m.ID, `{"nope":1}`, 400)
	f.wantError(t, "PATCH", "/api/memory/"+m.ID, `{"importance":"high"}`, 400)
	f.wantError(t, "PATCH", "/api/memory/abc", `{}`, 400)
	f.wantError(t, "PATCH", "/api/memory/404", `{"content":"y"}`, 404)
	if f.events.count() != base {
		t.Errorf("failed requests emitted %d events", f.events.count()-base)
	}
}

func TestRESTCore(t *testing.T) {
	f := newFixture(t)
	if body := f.do("GET", "/api/memory/core", "").Body.String(); !strings.Contains(body, `"entries":[]`) {
		t.Errorf("empty profile: %s", body)
	}
	w := f.do("PUT", "/api/memory/core/diet", `{"value":"vegetarian"}`)
	var e Entry
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Key != "diet" || e.Value != "vegetarian" || e.UpdatedAt == "" {
		t.Fatalf("put = %d %s", w.Code, w.Body)
	}
	f.do("PUT", "/api/memory/core/Favorite%20Color", `{"value":"green"}`)
	var got struct{ Entries []Entry }
	if err := json.Unmarshal(f.do("GET", "/api/memory/core", "").Body.Bytes(), &got); err != nil || len(got.Entries) != 2 || got.Entries[0].Key != "diet" || got.Entries[1].Key != "favorite_color" {
		t.Fatalf("core = %+v %v", got, err)
	}
	base := f.events.count()
	f.wantError(t, "PUT", "/api/memory/core/diet", `{"value":""}`, 400)
	f.wantError(t, "PUT", "/api/memory/core/diet", `{}`, 400)
	f.wantError(t, "PUT", "/api/memory/core/diet", `{"value":"x","extra":1}`, 400)
	f.wantError(t, "DELETE", "/api/memory/core/unknown", "", 404)
	if w := f.do("DELETE", "/api/memory/core/diet", ""); w.Code != 204 {
		t.Errorf("delete = %d", w.Code)
	}
	if f.events.count() != base+1 {
		t.Errorf("events = %d, want only the successful delete", f.events.count()-base)
	}
}

func run(t *testing.T, f *fixture, name, args string) (agent.Result, error) {
	t.Helper()
	for _, tl := range f.svc.Tools() {
		if tl.Name == name {
			return tl.Run(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %q", name)
	return agent.Result{}, nil
}

func TestToolEffects(t *testing.T) {
	f := newFixture(t)
	if _, err := agent.NewRegistry(f.svc.Tools()...); err != nil {
		t.Fatal(err)
	}
	for _, tl := range f.svc.Tools() {
		want := agent.EffectLocal
		if tl.Name == "memory_search" {
			want = agent.EffectRead
		}
		if tl.Effect != want {
			t.Errorf("%s effect = %s", tl.Name, tl.Effect)
		}
	}
}

func TestToolsSaveSearchUpdateForget(t *testing.T) {
	f := newFixture(t)
	res, err := run(t, f, "memory_save", `{"content":"Rika is vegetarian","kind":"preference","importance":4}`)
	if err != nil || !strings.HasPrefix(res.Content, "Saved: [1] (preference) Rika is vegetarian") {
		t.Fatalf("save: %v %q", err, res.Content)
	}
	var card struct{ Items []Memory }
	if res.Card == nil || res.Card.Kind != "memory" || json.Unmarshal(res.Card.Data, &card) != nil || len(card.Items) != 1 || card.Items[0].ID != "1" {
		t.Errorf("card = %+v", res.Card)
	}

	// A near-duplicate is flagged, an exact one is not stored twice.
	res, err = run(t, f, "memory_save", `{"content":"Rika eats vegetarian food","kind":"preference"}`)
	if err != nil || !strings.Contains(res.Content, "Similar memories") || !strings.Contains(res.Content, "[1]") {
		t.Errorf("similar hint: %v %q", err, res.Content)
	}
	res, _ = run(t, f, "memory_save", `{"content":"rika is vegetarian","kind":"preference"}`)
	if !strings.HasPrefix(res.Content, "Already remembered") {
		t.Errorf("duplicate: %q", res.Content)
	}

	res, err = run(t, f, "memory_save", `{"content":"Interview at Acme","kind":"follow_up","follow_up_at":"2026-10-22T16:00"}`)
	if err != nil || !strings.Contains(res.Content, "ask on 2026-10-22T16:00:00-05:00") {
		t.Errorf("follow-up: %v %q", err, res.Content)
	}

	res, err = run(t, f, "memory_search", `{"query":"vegetarian"}`)
	if err != nil || !strings.Contains(res.Content, "[1]") || res.Card == nil || res.Card.Title != "What I remember" {
		t.Errorf("search: %v %q", err, res.Content)
	}
	res, err = run(t, f, "memory_search", `{"query":"submarines"}`)
	if err != nil || res.Content != "No matching memories." || res.Card != nil {
		t.Errorf("empty search: %v %+v", err, res)
	}

	res, err = run(t, f, "memory_update", `{"id":"3","follow_up_at":"","content":"Interview at Acme went well"}`)
	if err != nil || strings.Contains(res.Content, "ask on") || !strings.Contains(res.Content, "went well") {
		t.Errorf("update: %v %q", err, res.Content)
	}
	res, err = run(t, f, "memory_forget", `{"id":1}`)
	if err != nil || !strings.HasPrefix(res.Content, "Forgotten for good: [1]") {
		t.Errorf("forget: %v %q", err, res.Content)
	}
	if got, _ := f.svc.Search(t.Context(), "vegetarian", "", 5); len(got) != 1 || got[0].ID != "2" {
		t.Errorf("after forget: %q", contents(got))
	}

	for _, c := range []struct{ tool, args string }{
		{"memory_save", `{"content":"x","kind":"gossip"}`}, {"memory_save", `{"content":"x"}`}, {"memory_save", `{"kind":"fact"}`},
		{"memory_search", `{"query":"the"}`}, {"memory_search", `{}`},
		{"memory_update", `{}`}, {"memory_update", `{"id":99,"content":"x"}`},
		{"memory_forget", `{"id":99}`}, {"memory_forget", `{}`},
	} {
		if _, err := run(t, f, c.tool, c.args); err == nil {
			t.Errorf("%s %s accepted", c.tool, c.args)
		}
	}
}

func TestProfileTools(t *testing.T) {
	f := newFixture(t)
	res, err := run(t, f, "profile_set", `{"key":"Diet","value":"vegetarian"}`)
	if err != nil || res.Content != "Profile saved: diet = vegetarian" {
		t.Fatalf("set: %v %q", err, res.Content)
	}
	base := f.events.count()
	if _, err := run(t, f, "profile_set", `{"key":"diet","value":""}`); err == nil {
		t.Error("empty value accepted")
	}
	res, err = run(t, f, "profile_remove", `{"key":"DIET"}`)
	if err != nil || !strings.Contains(res.Content, "diet") {
		t.Errorf("remove: %v %q", err, res.Content)
	}
	if _, err := run(t, f, "profile_remove", `{"key":"diet"}`); err == nil {
		t.Error("removed a key that is gone")
	}
	if f.events.count() != base+1 {
		t.Errorf("events = %d", f.events.count()-base)
	}
}
