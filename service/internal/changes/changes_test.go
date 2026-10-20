package changes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

type recEvents struct {
	mu    sync.Mutex
	notes []proto.Notify
}

func (r *recEvents) Invalidate(...string) {}

func (r *recEvents) Notify(n proto.Notify) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
}

type fakePush struct {
	mu   sync.Mutex
	sent []push.Payload
}

func (p *fakePush) Send(_ context.Context, pl push.Payload, _ push.Urgency) (push.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, pl)
	return push.Result{}, nil
}

type fixture struct {
	store  *store.Store
	svc    *Service
	appr   *approvals.Service
	events *recEvents
	push   *fakePush
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{store: db, events: &recEvents{}, push: &fakePush{}, now: time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	f.appr, err = approvals.New(approvals.Options{Store: db, Location: time.UTC, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.svc, err = New(Options{Store: db, Approvals: f.appr, Location: time.UTC, Events: f.events, Push: f.push, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

var subjectRules = Proposal{Title: "Match mail rules on subject words", What: "Let a rule match words in the subject.",
	Why: "Marketing and receipts share a sender.", Kind: "feature"}

func (f *fixture) propose(t *testing.T, p Proposal) (ChangeRequest, approvals.Approval) {
	t.Helper()
	c, a, err := f.svc.Propose(t.Context(), p)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return c, a
}

func mustID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) decide(t *testing.T, a approvals.Approval, decision, note string) {
	t.Helper()
	if _, err := f.appr.Decide(t.Context(), mustID(t, a.ID), decision, note); err != nil {
		t.Fatalf("Decide: %v", err)
	}
}

func (f *fixture) get(t *testing.T, id string) ChangeRequest {
	t.Helper()
	c, err := f.svc.Get(t.Context(), mustID(t, id))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProposeAsksTheOwner(t *testing.T) {
	f := newFixture(t)
	c, a := f.propose(t, subjectRules)
	if c.Status != StatusProposed || c.ApprovalID == nil || *c.ApprovalID != a.ID {
		t.Fatalf("change = %+v, want proposed and linked to approval %s", c, a.ID)
	}
	if a.Kind != KindApproval || a.Title != "Build: Match mail rules on subject words?" || a.ScopeLabel != nil {
		t.Errorf("approval = %+v", a)
	}
	if !strings.Contains(a.Detail, "subject.\n\nWhy: Marketing") {
		t.Errorf("detail = %q, want what then why", a.Detail)
	}
	if want := f.now.Add(proposalTTL).Format(time.RFC3339); a.ExpiresAt != want {
		t.Errorf("expires_at = %s, want %s", a.ExpiresAt, want)
	}
}

// cancelOnPublish cancels the turn the moment the approval is published, as
// an owner interrupting her right then would.
type cancelOnPublish struct{ cancel context.CancelFunc }

func (c cancelOnPublish) Invalidate(...string) { c.cancel() }
func (c cancelOnPublish) Notify(proto.Notify)  {}

func TestProposeOutlivesACancelledTurn(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	appr, err := approvals.New(approvals.Options{Store: f.store, Location: time.UTC, Events: cancelOnPublish{cancel}, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Options{Store: f.store, Approvals: appr, Location: time.UTC, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	c, a, err := svc.Propose(ctx, subjectRules)
	if err != nil {
		t.Fatalf("Propose cancelled midway: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("the turn was never cancelled")
	}
	if c.ApprovalID == nil || *c.ApprovalID != a.ID {
		t.Fatalf("change %+v is not linked to approval %s", c, a.ID)
	}
	if _, err := appr.Decide(t.Context(), mustID(t, a.ID), approvals.DecisionDeny, "Not now."); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(t.Context(), mustID(t, c.ID)); got.Status != StatusDenied {
		t.Errorf("status after deny = %s, want denied", got.Status)
	}

	// A turn cancelled before it starts writes nothing at all.
	done, stop := context.WithCancel(t.Context())
	stop()
	if _, _, err := svc.Propose(done, subjectRules); err == nil {
		t.Error("proposed on a cancelled turn")
	}
	var changes, pending int
	db := f.store.DB()
	_ = db.QueryRow(`SELECT count(*) FROM change_requests`).Scan(&changes)
	_ = db.QueryRow(`SELECT count(*) FROM approvals`).Scan(&pending)
	if changes != 1 || pending != 1 {
		t.Errorf("rows after a cancelled proposal: %d changes, %d approvals; want 1 and 1", changes, pending)
	}
}

func TestProposeIsAllOrNothing(t *testing.T) {
	f := newFixture(t)
	db := f.store.DB()
	// Make the last write, the link, fail.
	if _, err := db.Exec(`CREATE TRIGGER fail_link BEFORE UPDATE OF approval_id ON change_requests
		BEGIN SELECT RAISE(ABORT, 'link failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Propose(t.Context(), subjectRules); err == nil {
		t.Fatal("Propose succeeded with a failing link")
	}
	var changes, pending int
	_ = db.QueryRow(`SELECT count(*) FROM change_requests`).Scan(&changes)
	_ = db.QueryRow(`SELECT count(*) FROM approvals`).Scan(&pending)
	if changes != 0 || pending != 0 {
		t.Errorf("after a failed proposal: %d changes, %d approvals left; want none", changes, pending)
	}
	if list, err := f.appr.List(t.Context(), approvals.ListPending); err != nil || len(list) != 0 {
		t.Errorf("pending approvals = %+v, %v", list, err)
	}
}

func TestLifecycle(t *testing.T) {
	f := newFixture(t)
	c, a := f.propose(t, subjectRules)
	id := mustID(t, c.ID)
	if _, err := f.svc.Claim(t.Context(), id); err == nil {
		t.Fatal("claimed a proposal the owner hasn't allowed")
	}
	f.decide(t, a, approvals.DecisionOnce, "")
	if got := f.get(t, c.ID); got.Status != StatusQueued {
		t.Fatalf("status after allow = %s, want queued", got.Status)
	}
	queued, err := f.svc.List(t.Context(), StatusQueued)
	if err != nil || len(queued) != 1 || queued[0].ID != c.ID {
		t.Fatalf("queued = %+v, %v", queued, err)
	}

	got, err := f.svc.Claim(t.Context(), id)
	if err != nil || got.Status != StatusWorking {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	var he *httpx.Error
	if _, err := f.svc.Claim(t.Context(), id); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("second claim = %v, want 409", err)
	}

	issue := "https://github.com/example/hekate/issues/12"
	got, err = f.svc.Progress(t.Context(), id, "Building", issue, "")
	if err != nil || got.Stage != "Building" || got.IssueURL == nil || *got.IssueURL != issue {
		t.Fatalf("progress = %+v, %v", got, err)
	}
	pr := "https://github.com/example/hekate/pull/13"
	got, err = f.svc.Progress(t.Context(), id, "PR open, in review", "", pr)
	if err != nil || *got.IssueURL != issue || got.PRURL == nil || *got.PRURL != pr {
		t.Fatalf("progress keeps earlier links: %+v, %v", got, err)
	}

	got, err = f.svc.Finish(t.Context(), id, StatusMerged, "Merged and deployed.", "", "")
	if err != nil || got.Status != StatusMerged || got.Stage != "" || got.FinishedAt == nil || *got.Result != "Merged and deployed." {
		t.Fatalf("finish = %+v, %v", got, err)
	}
	if len(f.events.notes) != 1 || f.events.notes[0].Kind != proto.NotifyKindChange || f.events.notes[0].Title != "Change merged" {
		t.Errorf("notifies = %+v", f.events.notes)
	}
	if len(f.push.sent) != 1 || f.push.sent[0].URL != "/?menu=changes" {
		t.Errorf("pushes = %+v", f.push.sent)
	}
	if _, err := f.svc.Progress(t.Context(), id, "Again", "", ""); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("progress after finish = %v, want 409", err)
	}
}

func TestDenyWithFeedbackAndRevise(t *testing.T) {
	f := newFixture(t)
	c, a := f.propose(t, subjectRules)
	f.decide(t, a, approvals.DecisionDeny, "Also cover the body, not just the subject.")
	got := f.get(t, c.ID)
	if got.Status != StatusDenied || got.Feedback == nil || !strings.Contains(*got.Feedback, "body") {
		t.Fatalf("denied change = %+v", got)
	}

	ctxText, err := f.svc.PromptContext(t.Context(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctxText, `#1 "Match mail rules on subject words" (feature): denied; the owner's reason: "Also cover the body`) {
		t.Errorf("context = %q", ctxText)
	}

	rev := subjectRules
	rev.Revises = mustID(t, c.ID)
	c2, a2 := f.propose(t, rev)
	if c2.Revises == nil || *c2.Revises != c.ID || !strings.HasPrefix(a2.Detail, "Revises #1.") {
		t.Errorf("revision = %+v / %q", c2, a2.Detail)
	}
	// Only a denied or expired proposal can be revised.
	rev.Revises = mustID(t, c2.ID)
	if _, _, err := f.svc.Propose(t.Context(), rev); err == nil {
		t.Error("revised a proposal still waiting on the owner")
	}
}

func TestExpiredProposal(t *testing.T) {
	f := newFixture(t)
	c, _ := f.propose(t, subjectRules)
	f.now = f.now.Add(proposalTTL + time.Minute)
	expired, err := f.svc.List(t.Context(), StatusExpired)
	if err != nil || len(expired) != 1 || expired[0].ID != c.ID {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
}

func TestDailyCap(t *testing.T) {
	f := newFixture(t)
	for range dailyCap {
		f.propose(t, subjectRules)
	}
	if _, _, err := f.svc.Propose(t.Context(), subjectRules); !errors.Is(err, ErrCap) {
		t.Fatalf("proposal over the cap = %v, want ErrCap", err)
	}
	res, err := f.svc.proposeTool().Run(t.Context(), mustJSON(t, subjectRules))
	if err != nil || !strings.Contains(res.Content, "most allowed") {
		t.Errorf("tool over the cap = %+v, %v; want a plain answer", res, err)
	}
	f.now = f.now.Add(capWindow)
	f.propose(t, subjectRules) // the window rolls
}

func TestProposalValidation(t *testing.T) {
	f := newFixture(t)
	for name, p := range map[string]Proposal{
		"no title":  {What: "x", Why: "y", Kind: "fix"},
		"no what":   {Title: "t", Why: "y", Kind: "fix"},
		"no why":    {Title: "t", What: "x", Kind: "fix"},
		"bad kind":  {Title: "t", What: "x", Why: "y", Kind: "refactor"},
		"long":      {Title: strings.Repeat("a", maxTitle+1), What: "x", Why: "y", Kind: "fix"},
		"no parent": {Title: "t", What: "x", Why: "y", Kind: "fix", Revises: 99},
	} {
		if _, _, err := f.svc.Propose(t.Context(), p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n, _ := f.svc.left(t.Context()); n != dailyCap {
		t.Errorf("refused proposals used the cap: %d left", n)
	}
}

func TestToolReturnsApprovalCard(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.proposeTool().Run(t.Context(), mustJSON(t, subjectRules))
	if err != nil {
		t.Fatal(err)
	}
	if res.Card == nil || res.Card.Kind != "approval" || !strings.Contains(res.Content, "change 1") {
		t.Errorf("result = %+v", res)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fixture) do(method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, req)
	return w
}

func TestREST(t *testing.T) {
	f := newFixture(t)
	c, a := f.propose(t, subjectRules)
	f.decide(t, a, approvals.DecisionOnce, "")
	base := "/api/changes/" + c.ID

	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/changes?status=queued", "", 200},
		{"GET", "/api/changes?status=bogus", "", 400},
		{"GET", "/api/changes/99", "", 404},
		{"POST", base + "/progress", `{"stage":"Building"}`, 409}, // not claimed yet
		{"POST", base + "/claim", "", 200},
		{"POST", base + "/claim", "", 409},
		{"POST", base + "/progress", `{"stage":""}`, 400},
		{"POST", base + "/progress", `{"stage":"PR open","pr_url":"https://evil.example/pr/1"}`, 400},
		{"POST", base + "/progress", `{"stage":"PR open","pr_url":"https://github.com/example/hekate/pull/2"}`, 200},
		{"POST", base + "/finish", `{"outcome":"done","result":"x"}`, 400},
		{"POST", base + "/finish", `{"outcome":"rejected"}`, 400},
		{"POST", base + "/finish", `{"outcome":"rejected","result":"The diff touched files outside the issue's scope."}`, 200},
		{"GET", base, "", 200},
	} {
		if w := f.do(tc.method, tc.path, tc.body); w.Code != tc.status {
			t.Errorf("%s %s %s = %d %s, want %d", tc.method, tc.path, tc.body, w.Code, w.Body, tc.status)
		}
	}
	var got ChangeRequest
	if err := json.Unmarshal(f.do("GET", base, "").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRejected || got.PRURL == nil || got.Result == nil {
		t.Errorf("final = %+v", got)
	}
	var list struct {
		Changes []ChangeRequest `json:"changes"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/changes", "").Body.Bytes(), &list); err != nil || len(list.Changes) != 1 {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestRetry(t *testing.T) {
	f := newFixture(t)
	c, a := f.propose(t, subjectRules)
	id := mustID(t, c.ID)
	var he *httpx.Error
	if _, err := f.svc.Retry(t.Context(), id); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("retrying a proposal = %v, want 409", err)
	}
	f.decide(t, a, approvals.DecisionOnce, "")
	if _, err := f.svc.Claim(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	issue, pr := "https://github.com/example/hekate/issues/1", "https://github.com/example/hekate/pull/2"
	if _, err := f.svc.Finish(t.Context(), id, StatusFailed, "Three review rounds weren't enough.", issue, pr); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.Retry(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusQueued || got.Result != nil || got.PRURL != nil || got.FinishedAt != nil || got.Stage != "" {
		t.Errorf("retried = %+v, want queued and cleared", got)
	}
	if got.IssueURL == nil || *got.IssueURL != issue {
		t.Errorf("issue = %v, want it kept", got.IssueURL)
	}
	if got.Attempts != 1 || got.PreviousResult == nil || *got.PreviousResult != "Three review rounds weren't enough." ||
		got.PreviousPRURL == nil || *got.PreviousPRURL != pr {
		t.Errorf("previous attempt = %d %v %v", got.Attempts, got.PreviousResult, got.PreviousPRURL)
	}
	if _, err := f.svc.Retry(t.Context(), id); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Errorf("retrying a queued change = %v, want 409", err)
	}

	// The next run counts as a second attempt; a merge can't be retried.
	if got, err := f.svc.Claim(t.Context(), id); err != nil || got.Attempts != 2 {
		t.Fatalf("second claim = %+v, %v", got, err)
	}
	if _, err := f.svc.Finish(t.Context(), id, StatusMerged, "Shipped.", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Retry(t.Context(), id); err == nil {
		t.Error("retried a merged change")
	}
	if w := f.do("POST", "/api/changes/"+c.ID+"/retry", ""); w.Code != http.StatusConflict {
		t.Errorf("POST retry on a merged change = %d, want 409", w.Code)
	}
}
