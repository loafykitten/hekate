package threads

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/review"
)

// fakeLab is the reviewer's view of the lab.
type fakeLab struct {
	mu   sync.Mutex
	cmds []string
}

func (l *fakeLab) ReviewerReady() bool { return true }

func (l *fakeLab) Inspect(_ context.Context, command string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cmds = append(l.cmds, command)
	return "Exit status 0. <untrusted_lab_output>services: {}</untrusted_lab_output>", nil
}

var reviewModel = config.Model{Name: "reviewer", Effort: "max"}

// labTool stands in for a lab tool by name: the review only looks at what was called.
func labTool(name string) agent.Tool {
	return agent.Tool{Name: name, Description: name, Schema: json.RawMessage(`{"type":"object"}`), Effect: agent.EffectExternal,
		Run: func(context.Context, json.RawMessage) (agent.Result, error) { return agent.Result{Content: "ok"}, nil }}
}

// reviewFixture is a thread service with lab tools and a reviewer; her replies and the reviewer's
// come from the scripts, in order.
func reviewFixture(t *testing.T, hers, its [][]string) (*fixture, *review.Reviewer) {
	t.Helper()
	var rv *review.Reviewer
	f := newFixture(t, func(o *Options) {
		o.Tools = append(o.Tools, labTool("lab_shell"), labTool("lab_write_file"))
		var err error
		rv, err = review.New(review.Options{Store: o.Store, LLM: o.LLM, Model: reviewModel, Lab: &fakeLab{}})
		if err != nil {
			t.Fatal(err)
		}
		o.Reviewer = rv
	})
	var mu sync.Mutex
	f.model.mu.Lock()
	f.model.script = func(req modelReq) []string {
		mu.Lock()
		defer mu.Unlock()
		next := &hers
		if req.Model == reviewModel.Name {
			next = &its
		}
		if len(*next) == 0 {
			return reply("(out of script)")
		}
		r := (*next)[0]
		*next = (*next)[1:]
		return r
	}
	f.model.mu.Unlock()
	return f, rv
}

// handOver is her calling finish_work.
func handOver(summary string) []string {
	b, _ := json.Marshal(map[string]string{"summary": summary})
	return callTool("finish_work", string(b))
}

func reviews(t *testing.T, rv *review.Reviewer, thread string, turns ...string) []proto.Review {
	t.Helper()
	got, err := rv.ForTurns(t.Context(), thread, turns)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func turnIDs(t *testing.T, f *fixture, thread string) []string {
	t.Helper()
	var ids []string
	for _, e := range ofType[proto.TurnStart](f.hub.inner(thread)) {
		ids = append(ids, e.TurnID)
	}
	return ids
}

func TestReviewChain(t *testing.T) {
	report1 := `Here's my report:
{"verdict": "looks_good", "summary": "The stack is drafted. Its UI has no login.",
 "findings": [
  {"severity": "high", "title": "JDownloader's UI has no login", "claim": "ready", "evidence_call_ids": [1], "evidence": "no WEB_AUTHENTICATION", "fix": "turn it on"},
  {"severity": "high", "title": "Made up", "evidence_call_ids": [9], "evidence": "call 9 never ran", "fix": "-"}],
 "verified_ok": ["the tags exist"]}}`
	report2 := "```json\n" + `{"verdict": "looks_good", "summary": "The login is on now.", "findings": [
  {"severity": "low", "title": "Mullvad has no port forwarding", "evidence_call_ids": [], "evidence": "unverified knowledge: dropped in 2023", "fix": "say so"}],
 "verified_ok": ["WEB_AUTHENTICATION=1"]}` + "\n```"
	f, rv := reviewFixture(t,
		[][]string{
			callTool("lab_write_file", `{"path":"/opt/stacks/dl/compose.yaml","content":"services: {}"}`), handOver("JDownloader runs at :5800."),
			reply("It's ready."),
			callTool("lab_write_file", `{"path":"/opt/stacks/dl/compose.yaml","content":"services: {}"}`), reply("Login added."),
			reply("Noted: I'll say so."),
		},
		[][]string{callTool("lab_inspect", `{"command":"cat /opt/stacks/dl/compose.yaml"}`), reply(report1), reply(report2)},
	)
	th, err := f.svc.Create(t.Context(), "", "", OriginOwner)
	if err != nil {
		t.Fatal(err)
	}
	f.say(t, th.ID, "set up a download client")
	waitFor(t, "three turns and two reviews", func() bool {
		ids := turnIDs(t, f, th.ID)
		if len(ids) < 3 || !f.svc.idle(th.ID) {
			return false
		}
		got := reviews(t, rv, th.ID, ids...)
		return len(got) == 2 && got[1].Status == proto.ReviewDone && got[1].FollowupTurnID != ""
	})
	waitFor(t, "the last turn", func() bool { r, _ := f.svc.running(th.ID); return !r })
	ids := turnIDs(t, f, th.ID)
	if len(ids) != 3 {
		t.Fatalf("turns %v, want 3: hers, the first review's, the second's", ids)
	}
	got := reviews(t, rv, th.ID, ids...)
	r1, r2 := got[0], got[1]
	if r1.TurnID != ids[0] || r1.Round != 1 || r1.FollowupTurnID != ids[1] || r1.Status != proto.ReviewDone {
		t.Errorf("first review %+v", r1)
	}
	if len(r1.Findings) != 1 || r1.Findings[0].Title != "JDownloader's UI has no login" ||
		!slices.Equal(r1.Findings[0].Commands, []string{"cat /opt/stacks/dl/compose.yaml"}) || r1.Findings[0].Unverified {
		t.Errorf("first review's findings %+v: the made-up one should go, the real one cite its command", r1.Findings)
	}
	if r1.Verdict != review.VerdictChanges || r1.Commands != 1 {
		t.Errorf("first review verdict %q, commands %d: a high finding can't look good", r1.Verdict, r1.Commands)
	}
	if r2.TurnID != ids[1] || r2.Round != 2 || r2.FollowupTurnID != ids[2] || len(r2.Findings) != 1 || !r2.Findings[0].Unverified {
		t.Errorf("second review %+v", r2)
	}
	// Her follow-up turns show nothing from the owner; the model reads the findings.
	starts := ofType[proto.TurnStart](f.hub.inner(th.ID))
	if starts[1].Origin != proto.OriginReview || starts[1].UserText != "" {
		t.Errorf("follow-up turn start %+v", starts[1])
	}
	var content string
	if err := f.db.DB().QueryRowContext(t.Context(), `SELECT content FROM messages WHERE turn_id = ? AND role = 'user'`, ids[1]).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Not from Sam") || !strings.Contains(content, "JDownloader's UI has no login") {
		t.Errorf("her follow-up input:\n%s", content)
	}
	msgs, _, _, err := f.svc.Messages(t.Context(), th.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == "user" && m.TurnID != ids[0] {
			t.Errorf("a follow-up shows an owner message: %+v", m)
		}
	}
	// The first round reads her handover and gets a budget for one change; the second knows the
	// first round's findings, and gets only what her fixes did.
	var first, second string
	for _, r := range f.model.requests() {
		if r.Model == reviewModel.Name {
			b, _ := json.Marshal(r.Messages)
			if first == "" {
				first = string(b)
			}
			second = string(b)
		}
	}
	if !strings.Contains(first, "JDownloader runs at :5800.") || !strings.Contains(first, "You have 14 commands") {
		t.Errorf("the first review's request lacks her handover or its budget: %s", first)
	}
	if !strings.Contains(second, "first round") || !strings.Contains(second, "lab_write_file: wrote /opt/stacks/dl/compose.yaml") {
		t.Errorf("the second review's request lacks the first round or her actions: %s", second)
	}
	// Each review was announced running, then ended.
	var states []string
	for _, e := range ofType[proto.ReviewMsg](f.hub.inner(th.ID)) {
		states = append(states, e.Review.Status)
	}
	if !slices.Equal(states, []string{"running", "done", "running", "done"}) {
		t.Errorf("review events %v", states)
	}
}

func TestReadOnlyTurnIsNotReviewed(t *testing.T) {
	f, rv := reviewFixture(t, [][]string{callTool("lab_shell", `{"command":"docker ps; df -h"}`), reply("Two containers run.")}, nil)
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "what's running?")
	waitFor(t, "her reply", func() bool { return len(ofType[proto.TurnEnd](f.hub.inner(th.ID))) == 1 && f.svc.idle(th.ID) })
	if r, rev := f.svc.running(th.ID); r || rev {
		t.Errorf("running %v reviewing %v after a look", r, rev)
	}
	if got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...); len(got) != 0 {
		t.Errorf("a look was reviewed: %+v", got)
	}
}

func TestStopAReview(t *testing.T) {
	f, rv := reviewFixture(t,
		[][]string{callTool("lab_shell", `{"command":"docker compose up -d"}`), handOver("It runs."), reply("Started.")},
		[][]string{{hang}},
	)
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "start it")
	waitFor(t, "the review", func() bool { _, rev := f.svc.running(th.ID); return rev })
	got, _ := f.svc.Get(t.Context(), th.ID)
	if !got.Running || !got.Reviewing {
		t.Errorf("thread during its review %+v", got)
	}
	// New input waits for the review.
	f.say(t, th.ID, "and another thing")
	errs := ofType[proto.Error](f.hub.inner(th.ID))
	if len(errs) != 1 || errs[0].Message != inReview {
		t.Errorf("input during a review: %+v", errs)
	}
	in, _ := f.svc.Input(t.Context(), th.ID)
	in.Interrupt("", -1)
	waitFor(t, "the review to stop", func() bool { r, _ := f.svc.running(th.ID); return !r })
	all := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...)
	if len(all) != 1 || all[0].Status != proto.ReviewStopped || all[0].FollowupTurnID != "" {
		t.Errorf("stopped review %+v", all)
	}
	if n := len(turnIDs(t, f, th.ID)); n != 1 {
		t.Errorf("%d turns: a stopped review hands her nothing", n)
	}
}

func TestThreadStopToolStopsAReview(t *testing.T) {
	f, rv := reviewFixture(t,
		[][]string{callTool("lab_write_file", `{"path":"/opt/stacks/x/compose.yaml","content":"x"}`), handOver("x is set up."), reply("Written.")},
		[][]string{{hang}},
	)
	th, _ := f.svc.Create(t.Context(), "Set up x", "", OriginOwner)
	f.say(t, th.ID, "set up x")
	waitFor(t, "the review", func() bool { _, rev := f.svc.running(th.ID); return rev })
	res, err := f.svc.stopTool().Run(t.Context(), json.RawMessage(`{"thread":"`+th.ID+`"}`))
	if err != nil || !strings.Contains(res.Content, "Stopped") {
		t.Fatalf("thread_stop: %v %q", err, res.Content)
	}
	waitFor(t, "the review to stop", func() bool { r, _ := f.svc.running(th.ID); return !r })
	if all := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...); len(all) != 1 || all[0].Status != proto.ReviewStopped {
		t.Errorf("review after thread_stop %+v", all)
	}
}

// Steps along the way wait; the turn that hands the work over has all of it reviewed at once.
func TestStepsWaitForHandover(t *testing.T) {
	f, rv := reviewFixture(t,
		[][]string{
			callTool("lab_write_file", `{"path":"/opt/stacks/dl/compose.yaml","content":"x"}`), reply("Drafted. Which VPN do you use?"),
			callTool("lab_shell", `{"command":"docker compose up -d"}`), handOver("The stack runs behind the VPN."), reply("Done."),
		},
		[][]string{reply(`{"verdict":"looks_good","summary":"Fine.","findings":[],"verified_ok":["it runs"]}`)},
	)
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "set up downloads")
	waitFor(t, "her first reply", func() bool { return len(f.turnEnds(th.ID)) == 1 && f.svc.idle(th.ID) })
	if r, rev := f.svc.running(th.ID); r || rev {
		t.Fatal("a step along the way was reviewed")
	}
	f.say(t, th.ID, "PIA")
	waitFor(t, "the review", func() bool {
		got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...)
		return len(got) == 1 && got[0].Status == proto.ReviewDone
	})
	ids := turnIDs(t, f, th.ID)
	if got := reviews(t, rv, th.ID, ids...); got[0].TurnID != ids[1] || got[0].Round != 1 {
		t.Errorf("review %+v, want round 1 of the handing-over turn", got[0])
	}
	var req string
	for _, r := range f.model.requests() {
		if r.Model == reviewModel.Name {
			b, _ := json.Marshal(r.Messages)
			req = string(b)
		}
	}
	for _, want := range []string{"wrote /opt/stacks/dl/compose.yaml", "docker compose up -d", "The stack runs behind the VPN.",
		`after the owner said \"PIA\"`, "You have 16 commands"} {
		if !strings.Contains(req, want) {
			t.Errorf("the review's request lacks %q: %s", want, req)
		}
	}
}

// Changes she never hands over are reviewed once the thread goes quiet.
func TestQuietThreadIsReviewed(t *testing.T) {
	f, rv := reviewFixture(t,
		[][]string{callTool("lab_shell", `{"command":"docker compose up -d"}`), reply("Started it; tell me if the UI loads.")},
		[][]string{reply(`{"verdict":"looks_good","summary":"Fine.","findings":[],"verified_ok":[]}`)},
	)
	f.svc.quietAfter = 300 * time.Millisecond
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "start it")
	waitFor(t, "the quiet review", func() bool {
		got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...)
		return len(got) == 1 && got[0].Status == proto.ReviewDone
	})
	if got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...); got[0].Round != 1 || got[0].TurnID != turnIDs(t, f, th.ID)[0] {
		t.Errorf("quiet review %+v", got[0])
	}
	// The owner hears it found nothing, just after it's recorded.
	waitFor(t, "the owner told", func() bool { return len(f.events.notified()) > 0 })
}

// A turn that only hands over (the changes came in earlier turns) starts the review.
func TestHandoverAlone(t *testing.T) {
	f, rv := reviewFixture(t,
		[][]string{
			callTool("lab_shell", `{"command":"docker compose up -d"}`), reply("Started; does the UI load for you?"),
			handOver("It runs and you've seen the UI."), reply("Great, it's done."),
		},
		[][]string{reply(`{"verdict":"looks_good","summary":"Fine.","findings":[],"verified_ok":[]}`)},
	)
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "start it")
	waitFor(t, "her first reply", func() bool { return len(f.turnEnds(th.ID)) == 1 && f.svc.idle(th.ID) })
	f.say(t, th.ID, "it loads")
	waitFor(t, "the review", func() bool {
		got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...)
		return len(got) == 1 && got[0].Status == proto.ReviewDone && got[0].TurnID == turnIDs(t, f, th.ID)[1]
	})
}

// Changes from a turn that failed, or that were left when the service stopped, still get the
// quiet review.
func TestPendingSurvivesFailureAndRestart(t *testing.T) {
	f, rv := reviewFixture(t,
		[][]string{callTool("lab_shell", `{"command":"docker compose up -d"}`), {hang}},
		[][]string{reply(`{"verdict":"looks_good","summary":"Fine.","findings":[],"verified_ok":[]}`)},
	)
	f.svc.quietAfter = time.Hour
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "start it")
	waitFor(t, "her change", func() bool {
		var n int
		_ = f.db.DB().QueryRow(`SELECT count(*) FROM activity WHERE tool = 'lab_shell'`).Scan(&n)
		return n == 1
	})
	in, _ := f.svc.Input(t.Context(), th.ID)
	in.Interrupt("", -1)
	waitFor(t, "the stop", func() bool { return len(f.turnEnds(th.ID)) == 1 && f.svc.idle(th.ID) })
	f.svc.mu.Lock()
	c := f.svc.convs[th.ID]
	f.svc.mu.Unlock()
	c.mu.Lock()
	armed := c.quiet != nil
	c.mu.Unlock()
	if !armed {
		t.Fatal("a stopped turn's changes aren't waiting for review")
	}

	// A new service on the same database, as after a restart, arms the wait again.
	opts := f.svc.opts
	opts.ReviewQuiet = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	again, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = again.Shutdown(sctx)
	})
	waitFor(t, "the review after the restart", func() bool {
		got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...)
		return len(got) == 1 && got[0].Status == proto.ReviewDone
	})
}

// The last answer to a chain of reviews isn't reviewed: not when it ends, not once the thread is
// quiet, not after a restart. It goes with her next piece of work.
func TestChainEndStaysEnded(t *testing.T) {
	finding := reply(`{"verdict":"needs_changes","summary":"One thing.","findings":[{"severity":"medium","title":"Pin the tag",` +
		`"evidence_call_ids":[],"evidence":"unverified knowledge: latest moves","fix":"pin it"}],"verified_ok":[]}`)
	write := callTool("lab_write_file", `{"path":"/opt/stacks/x/compose.yaml","content":"x"}`)
	f, rv := reviewFixture(t,
		[][]string{write, handOver("x runs."), reply("Done."), write, reply("Pinned."), write, reply("Pinned again.")},
		[][]string{finding, finding},
	)
	f.svc.quietAfter = 100 * time.Millisecond
	th, _ := f.svc.Create(t.Context(), "", "", OriginOwner)
	f.say(t, th.ID, "set up x")
	waitFor(t, "two rounds and her last answer", func() bool {
		r, _ := f.svc.running(th.ID)
		return len(f.turnEnds(th.ID)) == 3 && !r
	})
	opts := f.svc.opts
	opts.ReviewQuiet = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	again, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = again.Shutdown(sctx)
	})
	time.Sleep(600 * time.Millisecond)
	if got := reviews(t, rv, th.ID, turnIDs(t, f, th.ID)...); len(got) != 2 {
		t.Errorf("%d reviews, want the two rounds only", len(got))
	}
}
