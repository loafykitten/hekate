// Integration tests against a real CalDAV server (Radicale). They are skipped
// unless HEKATE_CALDAV_TEST_URL is set, so CI does not need a server.
//
// To run them:
//
//	python3 -m venv /tmp/radicale-venv && /tmp/radicale-venv/bin/pip install radicale
//	mkdir -p /tmp/radicale-data && printf 'tester:secret\n' > /tmp/radicale-users
//	/tmp/radicale-venv/bin/radicale --hosts 127.0.0.1:5917 \
//	    --storage-filesystem-folder /tmp/radicale-data \
//	    --auth-type htpasswd --auth-htpasswd-filename /tmp/radicale-users \
//	    --auth-htpasswd-encryption plain &
//	HEKATE_CALDAV_TEST_URL=http://127.0.0.1:5917/ HEKATE_CALDAV_TEST_USER=tester \
//	    HEKATE_CALDAV_TEST_PASSWORD=secret go test -race -run Integration ./internal/calendar/
//
// Every test creates its own uniquely named calendar, so runs do not collide
// and the server needs no cleanup between them.
package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/google/uuid"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
)

type integ struct {
	url, user, pass string
}

func integEnv(t *testing.T) integ {
	t.Helper()
	u := os.Getenv("HEKATE_CALDAV_TEST_URL")
	if u == "" {
		t.Skip("HEKATE_CALDAV_TEST_URL not set; see the comment at the top of this file")
	}
	return integ{url: u, user: os.Getenv("HEKATE_CALDAV_TEST_USER"), pass: os.Getenv("HEKATE_CALDAV_TEST_PASSWORD")}
}

type changes struct {
	mu sync.Mutex
	n  int
}

func (c *changes) fn(domain string) {
	if domain == "calendar" {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
}

func (c *changes) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (e integ) service(t *testing.T, name string, hc *http.Client, ch *changes, now time.Time) *Service {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{
		URL: e.url, Username: e.user, Password: config.Secret(e.pass), CalendarName: name,
		Location: loc, HTTPClient: hc, Now: func() time.Time { return now },
	}
	if ch != nil {
		o.OnChange = ch.fn
	}
	return New(o)
}

func uniqueName() string { return "Hekate test " + uuid.NewString()[:8] }

var testNow = time.Date(2026, 10, 19, 9, 0, 0, 0, time.FixedZone("CDT", -5*3600))

func TestIntegrationCreateAndLifecycle(t *testing.T) {
	env := integEnv(t)
	ctx := context.Background()
	ch := &changes{}
	name := uniqueName()
	s := env.service(t, name, nil, ch, testNow)

	// The calendar does not exist yet; the first call creates it.
	evs, err := s.Events(ctx, testNow, testNow.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("Events on a fresh server: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("fresh calendar has events: %v", evs)
	}
	col, err := s.dav.collection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cals, err := s.dav.cd.FindCalendars(ctx, ensureSlash(col[:strings.LastIndex(strings.TrimSuffix(col, "/"), "/")]))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, c := range cals {
		if c.Name == name {
			found++
			if len(c.SupportedComponentSet) != 1 || c.SupportedComponentSet[0] != "VEVENT" {
				t.Errorf("components = %v", c.SupportedComponentSet)
			}
		}
	}
	if found != 1 {
		t.Fatalf("calendars named %q: %d", name, found)
	}
	// A second service with the same name reuses it.
	s2 := env.service(t, name, nil, nil, testNow)
	col2, err := s2.dav.collection(ctx)
	if err != nil || col2 != col {
		t.Fatalf("second collection = %q, %v; want %q", col2, err, col)
	}

	ev, err := s.Create(ctx, EventInput{Title: "Dentist, Dr. Fox", Start: "2026-10-20T15:00", Location: "Main St", Notes: "line1\nline2"})
	if err != nil {
		t.Fatal(err)
	}
	if !ev.CreatedByHekate || ev.Instance != nil || ev.Start != "2026-10-20T15:00:00-05:00" || ev.End != "2026-10-20T16:00:00-05:00" {
		t.Errorf("created = %+v", ev)
	}
	if ch.count() != 1 {
		t.Errorf("changes after create = %d", ch.count())
	}

	allDay, err := s.Create(ctx, EventInput{Title: "Holiday", Start: "2026-10-22"})
	if err != nil || !allDay.AllDay || allDay.End != "2026-10-23" {
		t.Fatalf("all-day = %+v, %v", allDay, err)
	}

	evs, err = s.Events(ctx, testNow, testNow.AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].UID != ev.UID || evs[1].UID != allDay.UID {
		t.Fatalf("list = %+v", evs)
	}
	if evs[0].Title != "Dentist, Dr. Fox" || evs[0].Notes != "line1\nline2" || evs[0].Location != "Main St" {
		t.Errorf("round trip = %+v", evs[0])
	}

	// Update: move it, keep its length.
	start := "2026-10-21T10:30"
	title := "Dentist (rescheduled)"
	up, err := s.Update(ctx, ev.UID, EventPatch{Title: &title, Start: &start}, true)
	if err != nil {
		t.Fatal(err)
	}
	if up.Start != "2026-10-21T10:30:00-05:00" || up.End != "2026-10-21T11:30:00-05:00" || up.Title != title {
		t.Errorf("updated = %+v", up)
	}
	evs, _ = s.Events(ctx, testNow, testNow.AddDate(0, 0, 7))
	if len(evs) != 2 || evs[0].Title != title {
		t.Errorf("after update = %+v", evs)
	}

	// Delete.
	if err := s.Delete(ctx, ev.UID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, ev.UID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	evs, _ = s.Events(ctx, testNow, testNow.AddDate(0, 0, 7))
	if len(evs) != 1 || evs[0].UID != allDay.UID {
		t.Errorf("after delete = %+v", evs)
	}
	if got := ch.count(); got != 4 { // 2 creates, 1 update, 1 delete
		t.Errorf("changes = %d, want 4", got)
	}
}

// putRaw stores an event written "by another client" at an arbitrary path.
func putRaw(t *testing.T, s *Service, name, ics string) {
	t.Helper()
	ctx := context.Background()
	col, err := s.dav.collection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(ics, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.dav.put(ctx, col+name+".ics", cal, ""); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationRecurringThroughServer(t *testing.T) {
	env := integEnv(t)
	ctx := context.Background()
	s := env.service(t, uniqueName(), nil, nil, testNow)

	// Written by another client: weekly, one cancelled instance, one moved.
	putRaw(t, s, "from-phone", weekly)

	from := time.Date(2026, 10, 19, 0, 0, 0, 0, s.loc)
	to := time.Date(2026, 11, 24, 0, 0, 0, 0, s.loc)
	evs, err := s.Events(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		got = append(got, e.Start)
	}
	want := []string{"2026-10-20T15:00:00-05:00", "2026-11-03T15:00:00-06:00", "2026-11-10T16:30:00-06:00", "2026-11-17T15:00:00-06:00"}
	if !equal(got, want) {
		t.Fatalf("instances = %v\nwant       %v", got, want)
	}
	if evs[0].CreatedByHekate {
		t.Error("an event from another client must not count as created by Hekate")
	}

	// A window that only the moved instance falls in still finds the object.
	evs, err = s.Events(ctx, time.Date(2026, 11, 10, 16, 0, 0, 0, s.loc), time.Date(2026, 11, 10, 18, 0, 0, 0, s.loc))
	if err != nil || len(evs) != 1 || evs[0].Title != "Standup (moved)" {
		t.Fatalf("moved window = %+v, %v", evs, err)
	}

	// Found by uid although it is not stored at <uid>.ics.
	if ev, err := s.Get(ctx, "weekly-1"); err != nil || ev.Title != "Standup, weekly" {
		t.Fatalf("Get = %+v, %v", ev, err)
	}

	// Tools refuse to touch it.
	title := "hijack"
	if _, err := s.Update(ctx, "weekly-1", EventPatch{Title: &title}, true); !errors.Is(err, ErrNotOwned) {
		t.Errorf("tool-style update = %v", err)
	}
	if err := s.Delete(ctx, "weekly-1", true); !errors.Is(err, ErrNotOwned) {
		t.Errorf("tool-style delete = %v", err)
	}

	// The owner may (REST): rename keeps the exceptions.
	if _, err := s.Update(ctx, "weekly-1", EventPatch{Title: &title}, false); err != nil {
		t.Fatal(err)
	}
	evs, _ = s.Events(ctx, from, to)
	if len(evs) != 4 || evs[0].Title != "hijack" || evs[2].Title != "Standup (moved)" {
		t.Errorf("after rename: %d events, %+v", len(evs), evs)
	}
	// Moving the series clears the exceptions.
	start := "2026-10-20T09:00"
	if _, err := s.Update(ctx, "weekly-1", EventPatch{Start: &start}, false); err != nil {
		t.Fatal(err)
	}
	evs, _ = s.Events(ctx, from, to)
	if len(evs) != 5 { // 10/20, 10/27, 11/3, 11/10, 11/17
		t.Errorf("after reschedule: %d events", len(evs))
	}
	if err := s.Delete(ctx, "weekly-1", false); err != nil {
		t.Fatal(err)
	}
	if evs, _ = s.Events(ctx, from, to); len(evs) != 0 {
		t.Errorf("after delete: %+v", evs)
	}

	// A recurring event Hekate creates.
	ev, err := s.Create(ctx, EventInput{Title: "Water plants", Start: "2026-10-19T08:00", RRule: "FREQ=DAILY;COUNT=3"})
	if err != nil || ev.Instance == nil {
		t.Fatalf("create recurring = %+v, %v", ev, err)
	}
	if evs, _ = s.Events(ctx, from, to); len(evs) != 3 {
		t.Errorf("daily x3 = %d", len(evs))
	}
}

// injector wraps a transport and, on the first PUT with If-Match, first lets
// someone else change the same resource, so the PUT fails with 412.
type injector struct {
	base   http.RoundTripper
	user   string
	pass   string
	fired  atomic.Int32
	mutate func(body string) string
}

func (i *injector) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPut && req.Header.Get("If-Match") != "" && i.fired.Add(1) == 1 {
		get, _ := http.NewRequestWithContext(req.Context(), http.MethodGet, req.URL.String(), nil)
		get.SetBasicAuth(i.user, i.pass)
		resp, err := i.base.RoundTrip(get)
		if err != nil {
			return nil, err
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		put, _ := http.NewRequestWithContext(req.Context(), http.MethodPut, req.URL.String(), strings.NewReader(i.mutate(string(b))))
		put.SetBasicAuth(i.user, i.pass)
		put.Header.Set("Content-Type", "text/calendar")
		resp2, err := i.base.RoundTrip(put)
		if err != nil {
			return nil, err
		}
		_ = resp2.Body.Close()
	}
	return i.base.RoundTrip(req)
}

func TestIntegrationETagConflictRetry(t *testing.T) {
	env := integEnv(t)
	ctx := context.Background()
	inj := &injector{
		base: http.DefaultTransport, user: env.user, pass: env.pass,
		mutate: func(b string) string { return strings.Replace(b, "LOCATION:Main St", "LOCATION:Changed elsewhere", 1) },
	}
	hc := &http.Client{Transport: inj, Timeout: 20 * time.Second}
	s := env.service(t, uniqueName(), hc, nil, testNow)

	ev, err := s.Create(ctx, EventInput{Title: "Dentist", Start: "2026-10-20T15:00", Location: "Main St"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Dentist v2"
	up, err := s.Update(ctx, ev.UID, EventPatch{Title: &title}, true)
	if err != nil {
		t.Fatalf("update should retry after 412: %v", err)
	}
	if inj.fired.Load() < 2 {
		t.Fatalf("PUT with If-Match attempts = %d, want at least 2", inj.fired.Load())
	}
	if up.Title != title || up.Location != "Changed elsewhere" {
		t.Errorf("result %+v should carry both the patch and the concurrent edit", up)
	}

	// A conflict that keeps happening fails after one retry.
	always := &alwaysConflict{base: http.DefaultTransport}
	hc2 := &http.Client{Transport: always, Timeout: 20 * time.Second}
	s2 := env.service(t, uniqueName(), hc2, nil, testNow)
	ev2, err := s2.Create(ctx, EventInput{Title: "x", Start: "2026-10-20T15:00"})
	if err != nil {
		t.Fatal(err)
	}
	always.on.Store(true)
	if _, err := s2.Update(ctx, ev2.UID, EventPatch{Title: &title}, true); err == nil || !strings.Contains(err.Error(), "changed elsewhere") {
		t.Errorf("persistent conflict err = %v", err)
	}
	if always.puts.Load() != 2 {
		t.Errorf("attempts = %d, want 2", always.puts.Load())
	}
}

type alwaysConflict struct {
	base http.RoundTripper
	on   atomic.Bool
	puts atomic.Int32
}

func (a *alwaysConflict) RoundTrip(req *http.Request) (*http.Response, error) {
	if a.on.Load() && req.Method == http.MethodPut {
		a.puts.Add(1)
		return &http.Response{StatusCode: http.StatusPreconditionFailed, Status: "412 Precondition Failed",
			Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: req}, nil
	}
	return a.base.RoundTrip(req)
}

func TestIntegrationToolsAndREST(t *testing.T) {
	env := integEnv(t)
	ctx := context.Background()
	ch := &changes{}
	s := env.service(t, uniqueName(), nil, ch, testNow)
	tools := map[string]agent.Tool{}
	for _, tl := range s.Tools() {
		tools[tl.Name] = tl
	}
	run := func(name, args string) (agent.Result, error) {
		t.Helper()
		return tools[name].Run(ctx, json.RawMessage(args))
	}

	res, err := run("event_create", `{"title":"Lunch with Sam","start":"2026-10-21T12:00","location":"Cafe"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "Added") || res.Card == nil || res.Card.Kind != "events" {
		t.Fatalf("create result = %+v", res)
	}
	var card cardData
	if err := json.Unmarshal(res.Card.Data, &card); err != nil || len(card.Items) != 1 || len(card.Highlight) != 1 || card.Highlight[0] != card.Items[0].UID {
		t.Fatalf("card = %s (%v)", res.Card.Data, err)
	}
	uid := card.Items[0].UID

	res, err = run("calendar_events", `{"query":"lunch"}`)
	if err != nil || !strings.Contains(res.Content, "Lunch with Sam") || !strings.Contains(res.Content, uid) || res.Card == nil {
		t.Fatalf("list result = %+v, %v", res, err)
	}
	res, _ = run("calendar_events", `{"query":"nothing like this"}`)
	if !strings.Contains(res.Content, "No events") {
		t.Errorf("empty query result = %q", res.Content)
	}

	res, err = run("event_update", `{"uid":"`+uid+`","start":"2026-10-21T13:00","location":""}`)
	if err != nil || !strings.Contains(res.Content, "Updated") || !strings.Contains(res.Content, "13:00") {
		t.Fatalf("update = %+v, %v", res, err)
	}

	// An event from another client is protected.
	putRaw(t, s, "other", weekly)
	res, err = run("event_delete", `{"uid":"weekly-1"}`)
	if err != nil || !strings.Contains(res.Content, "not created by Hekate") {
		t.Fatalf("delete other = %+v, %v", res, err)
	}
	res, _ = run("event_update", `{"uid":"weekly-1","title":"x"}`)
	if !strings.Contains(res.Content, "not created by Hekate") {
		t.Errorf("update other = %q", res.Content)
	}

	// Prompt context shows today and tomorrow, and notices our own changes.
	text, err := s.PromptContext(ctx, testNow)
	if err != nil || !strings.Contains(text, "Calendar today (Mon Oct 19): nothing scheduled") || !strings.Contains(text, "Calendar tomorrow (Tue Oct 20): 15:00-16:00 Standup, weekly") {
		t.Fatalf("prompt = %q, %v", text, err)
	}
	if _, err := run("event_create", `{"title":"Coffee","start":"2026-10-19T14:00"}`); err != nil {
		t.Fatal(err)
	}
	text, _ = s.PromptContext(ctx, testNow)
	if !strings.Contains(text, "14:00-15:00 Coffee") {
		t.Errorf("prompt cache not invalidated by a mutation: %q", text)
	}

	res, err = run("event_delete", `{"uid":"`+uid+`"}`)
	if err != nil || !strings.Contains(res.Content, "Deleted") {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if _, err := run("event_delete", `{"uid":"`+uid+`"}`); err == nil || !strings.Contains(err.Error(), "no event") {
		t.Errorf("repeat delete err = %v", err)
	}

	// REST.
	mux := http.NewServeMux()
	s.Register(mux)
	do := func(method, path, body string) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = bytes.NewReader([]byte(body))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
		var m map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}
	rec, m := do("GET", "/api/calendar/status", "")
	if rec.Code != 200 || string(m["configured"]) != "true" {
		t.Errorf("status = %d %s", rec.Code, rec.Body)
	}
	rec, m = do("POST", "/api/calendar/events", `{"title":"Rest event","start":"2026-10-23","all_day":true,"notes":"n"}`)
	if rec.Code != 201 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	restUID := strings.Trim(string(m["uid"]), `"`)
	rec, m = do("GET", "/api/calendar/events?from=2026-10-23&to=2026-10-23", "")
	var list []Event
	_ = json.Unmarshal(m["events"], &list)
	if rec.Code != 200 || len(list) != 1 || list[0].Title != "Rest event" {
		t.Errorf("GET = %d %s", rec.Code, rec.Body)
	}
	rec, _ = do("PATCH", "/api/calendar/events/"+restUID, `{"title":"Renamed","uid":"`+restUID+`","instance":null,"created_by_hekate":true,"rrule":null}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Renamed"`) {
		t.Errorf("PATCH = %d %s", rec.Code, rec.Body)
	}
	rec, _ = do("PATCH", "/api/calendar/events/weekly-1", `{"title":"Owner renamed it"}`)
	if rec.Code != 200 {
		t.Errorf("owner PATCH of another client's event = %d %s", rec.Code, rec.Body)
	}
	rec, _ = do("PATCH", "/api/calendar/events/nope", `{"title":"x"}`)
	if rec.Code != 404 {
		t.Errorf("PATCH missing = %d", rec.Code)
	}
	rec, _ = do("DELETE", "/api/calendar/events/"+restUID, "")
	if rec.Code != 204 {
		t.Errorf("DELETE = %d %s", rec.Code, rec.Body)
	}
	rec, _ = do("DELETE", "/api/calendar/events/"+restUID, "")
	if rec.Code != 404 {
		t.Errorf("DELETE again = %d", rec.Code)
	}
}

func TestIntegrationWrongPassword(t *testing.T) {
	env := integEnv(t)
	bad := env
	bad.pass = "definitely-wrong"
	s := bad.service(t, uniqueName(), nil, nil, testNow)
	_, err := s.Events(context.Background(), testNow, testNow.AddDate(0, 0, 1))
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Error("error leaks the password")
	}
}
