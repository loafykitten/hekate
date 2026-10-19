package mail

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// deliverN delivers n messages from one sender, each a day apart ending an hour ago.
func (f *fixture) deliverN(from string, n int, seen ...int) {
	f.t.Helper()
	skip := map[int]bool{}
	for _, i := range seen {
		skip[i] = true
	}
	for i := range n {
		f.deliver("INBOX", mailSpec{
			from: from, subject: fmt.Sprintf("Update %d from %s", i, from), seen: skip[i],
			date: f.clock.Now().Add(-time.Hour - time.Duration(i)*24*time.Hour),
		})
	}
}

func rulesFrom(t *testing.T, f *fixture) []string {
	t.Helper()
	var out []string
	for _, r := range f.appr.requests() {
		if r.Kind != KindRuleCreate {
			continue
		}
		var spec RuleSpec
		if err := json.Unmarshal(r.Payload, &spec); err != nil {
			t.Fatal(err)
		}
		out = append(out, spec.From+" "+spec.Action)
	}
	return out
}

func TestNoiseReportProposesUnreadSenders(t *testing.T) {
	f := newFixture(t, withNoise)
	f.deliverN("Deals <deals@shop.example.net>", 5)                                           // 5, none read: proposed
	f.deliverN("Friend <friend@example.org>", 5, 2)                                           // one was read: not noise
	f.deliverN("Few <few@example.org>", 3)                                                    // too few
	f.deliverN("Alerts <a1@alerts.example.net>", 2)                                           // two addresses of one domain, 2 each:
	f.deliverN("Alerts <a2@alerts.example.net>", 2)                                           //   the domain qualifies together
	f.deliverN("Old <old@example.org>", 4)                                                    // has an unwanted-looking history but one needs_reply below
	f.deliver("INBOX", mailSpec{subject: "Re: please answer", from: "Old <old@example.org>"}) // classified needs_reply: never noise
	f.sweep()

	got := rulesFrom(t, f)
	want := map[string]bool{"deals@shop.example.net archive": true, "@alerts.example.net archive": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("proposals = %q, want the deals address and the alerts domain", got)
	}
	for _, r := range f.appr.requests() {
		if r.Scope != "" || r.ScopeLabel != "" {
			t.Errorf("a rule proposal can't be always-allowed, got scope %q", r.Scope)
		}
	}
	for _, r := range f.appr.requests() {
		if !r.Notify {
			t.Errorf("a proposal raised outside a conversation must notify the owner: %+v", r)
		}
	}
	// Weekly at most: nothing more the same week.
	f.clock.Advance(24 * time.Hour)
	f.deliverN("Deals2 <more@spam.example.net>", 5)
	f.sweep()
	if len(rulesFrom(t, f)) != 2 {
		t.Error("the report ran twice within a week")
	}
}

func TestNoiseReportCapsAtThree(t *testing.T) {
	f := newFixture(t, withNoise)
	for i := range 5 {
		f.deliverN(fmt.Sprintf("S%d <s%d@mail%d.example.net>", i, i, i), 4+i)
	}
	f.sweep()
	if got := rulesFrom(t, f); len(got) != 3 {
		t.Errorf("proposals = %q, want 3", got)
	}
	// The busiest are first.
	if got := rulesFrom(t, f); got[0] != "s4@mail4.example.net archive" {
		t.Errorf("order = %q", got)
	}
}

func TestNoiseReportSkipsRuledAndFreeMailDomains(t *testing.T) {
	f := newFixture(t, withNoise)
	f.deliverN("Ruled <ruled@news.example.net>", 5)
	f.deliverN("A <a@gmail.com>", 2)
	f.deliverN("B <b@gmail.com>", 2)
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@example.net", Action: "mark_read"}); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	if got := rulesFrom(t, f); len(got) != 0 {
		t.Errorf("proposals = %q: a covered sender and two strangers sharing a free-mail domain must not be proposed", got)
	}
}

func TestNoiseReportRemembersDenials(t *testing.T) {
	f := newFixture(t, withNoise)
	f.deliverN("Deals <deals@shop.example.net>", 4)
	f.sweep()
	reqs := f.appr.requests()
	if len(reqs) != 1 {
		t.Fatalf("proposals = %d", len(reqs))
	}
	var id string
	if err := f.store.DB().QueryRow(`SELECT CAST(id AS TEXT) FROM approvals ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	// While the proposal is pending, next week's report doesn't repeat it.
	f.clock.Advance(8 * 24 * time.Hour)
	f.deliverN("Deals <deals@shop.example.net>", 4)
	f.sweep()
	if len(f.appr.requests()) != 1 {
		t.Fatal("re-proposed a sender whose proposal is still pending")
	}

	// Denied: not again for 90 days.
	f.appr.decide(id, "denied")
	f.clock.Advance(8 * 24 * time.Hour)
	f.deliverN("Deals <deals@shop.example.net>", 4)
	f.sweep()
	if len(f.appr.requests()) != 1 {
		t.Fatal("re-proposed a denied sender")
	}
	f.clock.Advance(85 * 24 * time.Hour) // 101 days after the denial
	f.deliverN("Deals <deals@shop.example.net>", 4)
	f.sweep()
	if len(f.appr.requests()) != 2 {
		t.Errorf("proposals = %d, want the sender proposed again after the memory expires", len(f.appr.requests()))
	}
}

func TestExpiredProposalCanBeRepeated(t *testing.T) {
	f := newFixture(t, withNoise)
	f.deliverN("Deals <deals@shop.example.net>", 4)
	f.sweep()
	var id string
	_ = f.store.DB().QueryRow(`SELECT CAST(id AS TEXT) FROM approvals ORDER BY id DESC LIMIT 1`).Scan(&id)
	f.appr.decide(id, "expired")
	f.clock.Advance(8 * 24 * time.Hour)
	f.deliverN("Deals <deals@shop.example.net>", 4)
	f.sweep()
	if len(f.appr.requests()) != 2 {
		t.Errorf("proposals = %d, want a second after the first expired unanswered", len(f.appr.requests()))
	}
}

func withNoise(o *fixtureOpts) { o.noise = true }
