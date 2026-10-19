package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

type recEvents struct {
	mu      sync.Mutex
	invalid int
	notes   []proto.Notify
}

func (r *recEvents) Invalidate(...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalid++
}

func (r *recEvents) Notify(n proto.Notify) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
}

func (r *recEvents) notices() []proto.Notify {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.notes)
}

type recBroadcast struct {
	mu   sync.Mutex
	msgs []proto.ApprovalMsg
}

func (b *recBroadcast) Broadcast(msg any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := msg.(proto.ApprovalMsg); ok {
		b.msgs = append(b.msgs, m)
	}
}

func (b *recBroadcast) approvals(t *testing.T) []Approval {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Approval
	for _, m := range b.msgs {
		if m.Type != "approval" {
			t.Fatalf("message type = %q", m.Type)
		}
		var a Approval
		if err := json.Unmarshal(m.Approval, &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

type fakePush struct {
	mu   sync.Mutex
	sent []push.Payload
}

func (f *fakePush) Send(_ context.Context, p push.Payload, _ push.Urgency) (push.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, p)
	return push.Result{Sent: 1}, nil
}

// clock is a hand-wound clock: After channels fire when set passes them.
type clock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	asked   chan struct{} // receives once per After call
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newClock(now time.Time) *clock { return &clock{now: now, asked: make(chan struct{}, 100)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{c.now.Add(d), ch})
	c.asked <- struct{}{}
	return ch
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

type fixture struct {
	svc    *Service
	events *recEvents
	bcast  *recBroadcast
	push   *fakePush
	clock  *clock
	runs   atomic.Int32
}

// newFixture starts on Monday 2026-10-19 14:00 UTC with a "mail_trash" kind
// whose executor counts its runs and fails when the payload says "fail".
func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{events: &recEvents{}, bcast: &recBroadcast{}, push: &fakePush{},
		clock: newClock(time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC))}
	f.svc, err = New(Options{Store: db, Location: time.UTC, Events: f.events, Broadcast: f.bcast, Push: f.push,
		Now: f.clock.Now, After: f.clock.After})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Register("mail_trash", func(_ context.Context, p json.RawMessage) (string, error) {
		f.runs.Add(1)
		if strings.Contains(string(p), "fail") {
			return "", errors.New("server said no")
		}
		return "Moved 14 messages", nil
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) request(t *testing.T, scope string) Approval {
	t.Helper()
	a, err := f.svc.Request(t.Context(), Request{Kind: "mail_trash", Title: "Move 14 newsletters to Trash?",
		Scope: scope, ScopeLabel: "mail from example.com", Payload: json.RawMessage(`{"n":14}`)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fixture) decide(t *testing.T, a Approval, decision string) Approval {
	t.Helper()
	got, err := f.svc.Decide(t.Context(), mustID(a.ID), decision)
	if err != nil {
		t.Fatalf("Decide(%s, %s): %v", a.ID, decision, err)
	}
	return got
}

func mustID(s string) int64 {
	var id int64
	for _, c := range s {
		id = id*10 + int64(c-'0')
	}
	return id
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestRequestPendingThenAllowOnce(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "from:@example.com")
	if a.Status != StatusPending || a.ExpiresAt != "2026-10-20T14:00:00Z" || str(a.ScopeLabel) != "mail from example.com" || a.Result != nil {
		t.Fatalf("pending = %+v", a)
	}
	if got := f.bcast.approvals(t); len(got) != 1 || got[0].ID != a.ID || got[0].Status != StatusPending {
		t.Fatalf("broadcast = %+v", got)
	}
	if f.runs.Load() != 0 {
		t.Fatal("executor ran before the decision")
	}

	f.clock.advance(time.Minute)
	got := f.decide(t, a, DecisionOnce)
	if got.Status != StatusDone || str(got.Result) != "Moved 14 messages" || str(got.DecidedBy) != DecidedByOwner || str(got.DecidedAt) != "2026-10-19T14:01:00Z" {
		t.Fatalf("decided = %+v", got)
	}
	if rules, _ := f.svc.Rules(t.Context()); len(rules) != 0 {
		t.Errorf("once made rules: %+v", rules)
	}
	if b := f.bcast.approvals(t); len(b) != 3 || b[1].Status != StatusRunning || b[2].Status != StatusDone {
		t.Errorf("broadcasts = %+v", b)
	}
	if len(f.events.notices()) != 0 {
		t.Error("a conversation request sent a notify event")
	}
}

func TestNoScopeNoRule(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "")
	if a.ScopeLabel != nil {
		t.Errorf("scope_label = %v", a.ScopeLabel)
	}
	_, err := f.svc.Decide(t.Context(), mustID(a.ID), DecisionAlways)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		t.Fatalf("always without scope = %v", err)
	}
	if got, _ := f.svc.Get(t.Context(), mustID(a.ID)); got.Status != StatusPending || f.runs.Load() != 0 {
		t.Errorf("rejected decision changed state: %+v, runs %d", got, f.runs.Load())
	}
}

func TestUnknownKindAndDuplicateRegister(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Request(t.Context(), Request{Kind: "nope", Title: "x"}); err == nil {
		t.Error("unknown kind accepted")
	}
	if err := f.svc.Register("mail_trash", func(context.Context, json.RawMessage) (string, error) { return "", nil }); err == nil {
		t.Error("duplicate register accepted")
	}
}

func TestAlwaysStoresRuleAndNextRequestRunsAtOnce(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "from:@example.com")
	got := f.decide(t, a, DecisionAlways)
	if got.Status != StatusDone || f.runs.Load() != 1 {
		t.Fatalf("always = %+v, runs %d", got, f.runs.Load())
	}
	rules, err := f.svc.Rules(t.Context())
	if err != nil || len(rules) != 1 || rules[0].Kind != "mail_trash" || rules[0].ScopeLabel != "mail from example.com" || rules[0].Uses != 0 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}

	b := f.request(t, "from:@example.com")
	if b.Status != StatusDone || str(b.DecidedBy) != DecidedByRule || str(b.Result) != "Moved 14 messages" || f.runs.Load() != 2 {
		t.Fatalf("rule run = %+v, runs %d", b, f.runs.Load())
	}
	// A different scope still asks.
	if c := f.request(t, "from:@other.example"); c.Status != StatusPending {
		t.Errorf("other scope = %+v", c)
	}
	if rules, _ = f.svc.Rules(t.Context()); rules[0].Uses != 1 {
		t.Errorf("uses = %d, want 1", rules[0].Uses)
	}
	// Deciding "always" twice keeps one rule.
	d := f.request(t, "from:@other.example")
	f.decide(t, d, DecisionAlways)
	if rules, _ = f.svc.Rules(t.Context()); len(rules) != 2 {
		t.Errorf("rules = %+v", rules)
	}

	if err := f.svc.DeleteRule(t.Context(), mustID(rules[1].ID)); err != nil {
		t.Fatal(err)
	}
	if e := f.request(t, "from:@example.com"); e.Status != StatusPending {
		t.Errorf("after deleting the rule: %+v", e)
	}
	var he *httpx.Error
	if err := f.svc.DeleteRule(t.Context(), 999); !errors.As(err, &he) || he.Status != 404 {
		t.Errorf("delete missing rule = %v", err)
	}
}

func TestDeny(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "from:@example.com")
	got := f.decide(t, a, DecisionDeny)
	if got.Status != StatusDenied || got.Result != nil || str(got.DecidedBy) != DecidedByOwner || f.runs.Load() != 0 {
		t.Fatalf("denied = %+v, runs %d", got, f.runs.Load())
	}
	if _, err := f.svc.Decide(t.Context(), mustID(a.ID), DecisionOnce); !errors.Is(err, ErrNotPending) {
		t.Errorf("deciding twice = %v", err)
	}
}

func TestExecutorFailure(t *testing.T) {
	f := newFixture(t)
	a, err := f.svc.Request(t.Context(), Request{Kind: "mail_trash", Title: "Move?", Payload: json.RawMessage(`{"fail":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	got := f.decide(t, a, DecisionOnce)
	if got.Status != StatusFailed || str(got.Result) != "server said no" {
		t.Fatalf("failed = %+v", got)
	}
}

func TestExecutorPanicIsAFailure(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Register("boom", func(context.Context, json.RawMessage) (string, error) { panic("kaput") }); err != nil {
		t.Fatal(err)
	}
	a, err := f.svc.Request(t.Context(), Request{Kind: "boom", Title: "Boom?"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.decide(t, a, DecisionOnce); got.Status != StatusFailed || !strings.Contains(str(got.Result), "kaput") {
		t.Fatalf("panic = %+v", got)
	}
}

func TestExecutorOutlivesTheCaller(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Register("slow", func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return "finished", nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	a, err := f.svc.Request(t.Context(), Request{Kind: "slow", Title: "Slow?"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	// The caller's context ends mid-run; the action and its record must not.
	got, err := f.svc.Decide(ctx, mustID(a.ID), DecisionOnce)
	if err != nil || got.Status != StatusDone || str(got.Result) != "finished" {
		t.Fatalf("decide = %+v, %v", got, err)
	}
}

func TestDecideRunsOnce(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "")
	const n = 8
	var wg sync.WaitGroup
	var ok, conflict atomic.Int32
	for range n {
		wg.Go(func() {
			_, err := f.svc.Decide(t.Context(), mustID(a.ID), DecisionOnce)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrNotPending):
				conflict.Add(1)
			default:
				t.Errorf("Decide: %v", err)
			}
		})
	}
	wg.Wait()
	if f.runs.Load() != 1 || ok.Load() != 1 || conflict.Load() != n-1 {
		t.Fatalf("runs %d, ok %d, conflicts %d", f.runs.Load(), ok.Load(), conflict.Load())
	}
}

func TestDecideAfterExpiryIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "")
	f.clock.advance(25 * time.Hour) // nobody ran the expiry loop
	if _, err := f.svc.Decide(t.Context(), mustID(a.ID), DecisionOnce); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decide after expiry = %v", err)
	}
	if f.runs.Load() != 0 {
		t.Error("an expired action ran")
	}
}

func TestLazyExpiryOnRead(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "")
	f.clock.advance(24 * time.Hour)
	pending, err := f.svc.List(t.Context(), "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	got, _ := f.svc.Get(t.Context(), mustID(a.ID))
	if got.Status != StatusExpired || got.DecidedBy != nil || got.DecidedAt == nil {
		t.Fatalf("expired = %+v", got)
	}
	recent, _ := f.svc.List(t.Context(), ListRecent)
	if len(recent) != 1 || recent[0].ID != a.ID {
		t.Errorf("recent = %+v", recent)
	}
	if b := f.bcast.approvals(t); b[len(b)-1].Status != StatusExpired {
		t.Errorf("expiry not broadcast: %+v", b)
	}
}

func TestRunExpiresOnTime(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitAsked := func() {
		t.Helper()
		select {
		case <-f.clock.asked:
		case <-time.After(5 * time.Second):
			t.Fatal("expiry loop never went to sleep")
		}
	}
	waitAsked() // nothing pending: sleeps a minute
	a := f.request(t, "")
	waitAsked() // woken by the request; sleeps min(24h, 1m)
	f.clock.advance(24*time.Hour - time.Minute)
	waitAsked()
	if got, _ := f.svc.get(t.Context(), mustID(a.ID)); got.Status != StatusPending {
		t.Fatalf("expired early: %+v", got)
	}
	f.clock.advance(time.Minute)
	waitAsked() // swept, asleep again
	if got, _ := f.svc.get(t.Context(), mustID(a.ID)); got.Status != StatusExpired {
		t.Fatalf("not expired: %+v", got)
	}
	if b := f.bcast.approvals(t); b[len(b)-1].Status != StatusExpired {
		t.Errorf("last broadcast = %+v", b[len(b)-1])
	}
}

func TestNotify(t *testing.T) {
	f := newFixture(t)
	a, err := f.svc.Request(t.Context(), Request{Kind: "mail_trash", Title: "Always trash noise?", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	notes := f.events.notices()
	if len(notes) != 1 || notes[0].Kind != "approval" || notes[0].Body != "Always trash noise?" || notes[0].Card == nil ||
		notes[0].Card.Kind != "approval" || !strings.Contains(string(notes[0].Card.Data), `"id":"`+a.ID+`"`) {
		t.Fatalf("notes = %+v", notes)
	}
	if len(f.push.sent) != 1 || f.push.sent[0].Body != "Always trash noise?" {
		t.Errorf("push = %+v", f.push.sent)
	}
}

func TestPromptContext(t *testing.T) {
	f := newFixture(t)
	if got, err := f.svc.PromptContext(t.Context(), f.clock.Now()); err != nil || got != "" {
		t.Fatalf("empty context = %q, %v", got, err)
	}
	done := f.request(t, "from:@example.com")
	f.decide(t, done, DecisionOnce)
	denied := f.request(t, "")
	f.decide(t, denied, DecisionDeny)
	f.clock.advance(time.Hour)
	f.request(t, "")
	f.clock.advance(10 * time.Minute)

	got, err := f.svc.PromptContext(t.Context(), f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := `- Move 14 newsletters to Trash? (asked 10 min ago)

Decided recently:
- denied: Move 14 newsletters to Trash? (1 h ago)
- allowed: Move 14 newsletters to Trash? → Moved 14 messages (1 h ago)`
	if got != want {
		t.Errorf("context =\n%s\nwant\n%s", got, want)
	}

	f.clock.advance(13 * time.Hour)
	got, _ = f.svc.PromptContext(t.Context(), f.clock.Now())
	if strings.Contains(got, "Decided recently") || !strings.Contains(got, "asked 13 h ago") {
		t.Errorf("after 12h context = %q", got)
	}
}

func TestCard(t *testing.T) {
	c := Card(Approval{ID: "5", Kind: "mail_trash", Title: "Move?", Status: StatusPending})
	if c.Kind != "approval" || c.ID != "approval-5" || !strings.Contains(string(c.Data), `"status":"pending"`) {
		t.Errorf("card = %+v", c)
	}
}

// REST.

func (f *fixture) do(method, path, body, contentType string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	mux.ServeHTTP(w, req)
	return w
}

func (f *fixture) code(t *testing.T, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := f.do(method, path, body, "application/json")
	if w.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body, want)
	}
	return w
}

func TestREST(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "from:@example.com")

	var list struct{ Approvals []Approval }
	w := f.code(t, "GET", "/api/approvals", "", 200)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Approvals) != 1 || list.Approvals[0].ID != a.ID {
		t.Fatalf("pending list = %s", w.Body)
	}
	f.code(t, "GET", "/api/approvals?status=bogus", "", 400)

	f.code(t, "POST", "/api/approvals/"+a.ID+"/decide", `{"decision":"maybe"}`, 400)
	f.code(t, "POST", "/api/approvals/"+a.ID+"/decide", `{"decision":"always"}`, 200)
	f.code(t, "POST", "/api/approvals/"+a.ID+"/decide", `{"decision":"once"}`, 409)
	f.code(t, "POST", "/api/approvals/999/decide", `{"decision":"once"}`, 404)
	f.code(t, "POST", "/api/approvals/abc/decide", `{"decision":"once"}`, 400)
	if w := f.do("POST", "/api/approvals/"+a.ID+"/decide", `{"decision":"once"}`, "text/plain"); w.Code != 415 {
		t.Errorf("wrong content type = %d", w.Code)
	}

	b := f.request(t, "")
	f.code(t, "POST", "/api/approvals/"+b.ID+"/decide", `{"decision":"always"}`, 400)

	w = f.code(t, "GET", "/api/approvals?status=recent", "", 200)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Approvals) != 1 || list.Approvals[0].Status != StatusDone {
		t.Fatalf("recent list = %s", w.Body)
	}

	var rules struct{ Rules []Rule }
	w = f.code(t, "GET", "/api/approvals/rules", "", 200)
	if err := json.Unmarshal(w.Body.Bytes(), &rules); err != nil || len(rules.Rules) != 1 {
		t.Fatalf("rules = %s", w.Body)
	}
	f.code(t, "DELETE", "/api/approvals/rules/"+rules.Rules[0].ID, "", 204)
	f.code(t, "DELETE", "/api/approvals/rules/"+rules.Rules[0].ID, "", 404)
	if w = f.code(t, "GET", "/api/approvals/rules", "", 200); !strings.Contains(w.Body.String(), `"rules":[]`) {
		t.Errorf("rules after delete = %s", w.Body)
	}
}

func TestRestartFailsInterruptedActions(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "")
	id, _ := strconv.ParseInt(a.ID, 10, 64)
	if _, err := f.svc.db.ExecContext(t.Context(), `UPDATE approvals SET status = 'running' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.failInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Get(t.Context(), id)
	if err != nil || got.Status != StatusFailed || str(got.Result) != interruptedResult {
		t.Fatalf("after restart = %+v, %v", got, err)
	}
}
