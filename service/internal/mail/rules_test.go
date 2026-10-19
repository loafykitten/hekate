package mail

import (
	"strings"
	"testing"
)

func TestRuleSpecValidation(t *testing.T) {
	f := newFixture(t)
	bad := []RuleSpec{
		{Action: "archive"},                                    // no match
		{From: "example.com", Action: "archive"},               // not an address
		{From: "@com", Action: "archive"},                      // not a domain
		{From: "a b@example.com", Action: "archive"},           // spaces
		{Category: "spam", Action: "archive"},                  // unknown category
		{From: "@example.org", Action: "delete"},               // unknown action
		{From: "@example.org", Action: "move"},                 // move without folder
		{From: "@example.org", Action: "archive", Folder: "X"}, // folder without move
		{From: "@example.org", Action: "move", Folder: "INBOX"},
	}
	for _, sp := range bad {
		if _, err := f.svc.CreateRule(t.Context(), sp); err == nil {
			t.Errorf("accepted %+v", sp)
		}
	}
	r, err := f.svc.CreateRule(t.Context(), RuleSpec{From: " News@Example.org ", Category: "Newsletter", Action: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	if *r.Match.From != "news@example.org" || *r.Match.Category != "newsletter" || r.Label != "Archive newsletter mail from news@example.org" {
		t.Errorf("rule = %+v", r)
	}
	again, _ := f.svc.CreateRule(t.Context(), RuleSpec{From: "news@example.org", Category: "newsletter", Action: "archive"})
	if again.ID != r.ID {
		t.Error("an identical rule was created twice")
	}
	mv, _ := f.svc.CreateRule(t.Context(), RuleSpec{From: "@example.org", Action: "move", Folder: "Projects"})
	if mv.Label != "Move mail from example.org to Projects" {
		t.Errorf("label = %q", mv.Label)
	}
}

func TestRulesApplyToNewMailAndRecordHits(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Action: "archive"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateRule(ctx, RuleSpec{Category: "newsletter", Action: "mark_read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "boss@example.org", Category: "promo", Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Notice 1", from: "Shop <orders@shop.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Notice 2", from: "Shop <deals@mail.shop.example.net>"}) // a subdomain
	f.deliver("INBOX", mailSpec{subject: "Weekly digest", from: "Blog <blog@example.org>"})
	f.deliver("INBOX", mailSpec{subject: "Hello", from: "Friend <friend@example.org>"})
	f.deliver("INBOX", mailSpec{subject: "Big sale", from: "Someone <someone@example.org>"}) // promo, but from nobody a rule names
	f.deliver("INBOX", mailSpec{subject: "Big sale from the boss", from: "Boss <boss@example.org>"})
	f.sweep()

	if m := f.bySubject("Notice 1"); m.Folder != "Archive" {
		t.Errorf("domain rule: %+v", m)
	}
	if m := f.bySubject("Notice 2"); m.Folder != "Archive" {
		t.Errorf("domain rule should cover subdomains: %+v", m)
	}
	if m := f.bySubject("Weekly digest"); m.Folder != "INBOX" || m.Unread {
		t.Errorf("category rule: %+v", m)
	}
	if m := f.bySubject("Hello"); m.Folder != "INBOX" || !m.Unread {
		t.Errorf("unmatched message touched: %+v", m)
	}
	if m := f.bySubject("Big sale"); m.Folder != "INBOX" {
		t.Errorf("a rule needing both fields matched on one: %+v", m)
	}
	if m := f.bySubject("Big sale from the boss"); m.Folder != "Trash" {
		t.Errorf("two-field rule: %+v", m)
	}
	rules, _ := f.svc.Rules(ctx)
	hits := map[string]int{}
	for _, r := range rules {
		hits[r.Action] = r.Hits
		if r.Hits > 0 && r.LastHitAt == nil {
			t.Errorf("rule %s has hits but no last_hit_at", r.ID)
		}
	}
	if hits["archive"] != 2 || hits["mark_read"] != 1 || hits["trash"] != 1 {
		t.Errorf("hits = %v", hits)
	}
	// Rules are applied once: a rule added later leaves earlier mail alone.
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "friend@example.org", Action: "archive"}); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	if m := f.bySubject("Hello"); m.Folder != "INBOX" {
		t.Errorf("a new rule reached back to old mail: %+v", m)
	}
	f.deliver("INBOX", mailSpec{subject: "Hello again", from: "Friend <friend@example.org>"})
	f.sweep()
	if m := f.bySubject("Hello again"); m.Folder != "Archive" {
		t.Errorf("new mail should meet the new rule: %+v", m)
	}
}

func TestRuleFailureRetriesNextSweep(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@example.org", Action: "move", Folder: "Later"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Waits"})
	f.sweep()
	if m := f.bySubject("Waits"); m.Folder != "INBOX" {
		t.Fatalf("moved to a folder that doesn't exist: %+v", m)
	}
	if err := f.user.Create("Later", nil); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	if m := f.bySubject("Waits"); m.Folder != "Later" {
		t.Errorf("after the folder exists: %+v", m)
	}
}

func TestRulesWithoutModel(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	for _, sp := range []RuleSpec{{From: "@example.org", Action: "archive"}, {Category: "promo", Action: "trash"}} {
		if _, err := f.svc.CreateRule(t.Context(), sp); err != nil {
			t.Fatal(err)
		}
	}
	f.deliver("INBOX", mailSpec{subject: "Sale now"})
	f.sweep()
	if m := f.bySubject("Sale now"); m.Folder != "Archive" {
		t.Errorf("sender rules still work without a model: %+v", m)
	}
}

func TestSenderMatches(t *testing.T) {
	cases := []struct {
		from, addr string
		want       bool
	}{
		{"a@example.com", "A@Example.com", true},
		{"a@example.com", "b@example.com", false},
		{"@example.com", "x@example.com", true},
		{"@example.com", "x@news.example.com", true},
		{"@example.com", "x@notexample.com", false},
		{"@example.com", "x@example.com.evil.net", false},
		{"@example.com", "nobody", false},
	}
	for _, c := range cases {
		if got := senderMatches(c.from, c.addr); got != c.want {
			t.Errorf("senderMatches(%q, %q) = %v", c.from, c.addr, got)
		}
	}
	if !strings.HasPrefix(RuleSpec{From: "@example.com", Action: "trash"}.label(), "Trash") {
		t.Error("label verb")
	}
}
