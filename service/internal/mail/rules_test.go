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

func TestRuleExceptionValidationAndLabels(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", maxSubject+1)
	bad := [][]ExceptSpec{
		{{}},                    // nothing to match
		{{Subject: "   "}},      // blank words
		{{From: "example.com"}}, // not an address
		{{Category: "spam"}},    // unknown category
		{{Subject: long}},       // too long
		{{Subject: "a\x00b"}},   // control character
		make([]ExceptSpec, maxExceptions+1),
	}
	for i := range bad[len(bad)-1] {
		bad[len(bad)-1][i] = ExceptSpec{Subject: strings.Repeat("w", i+1)}
	}
	for _, ex := range bad {
		if _, err := f.svc.CreateRule(t.Context(), RuleSpec{Category: "promo", Action: "trash", Except: ex}); err == nil {
			t.Errorf("accepted exceptions %+v", ex)
		}
	}
	r, err := f.svc.CreateRule(t.Context(), RuleSpec{Category: "promo", Action: "trash", Except: []ExceptSpec{
		{Subject: "  Your   TRANSACTION "},
		{From: "Billing@Example.org", Category: "Receipt"},
		{Subject: "your transaction"}, // the same as the first, once folded
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "Trash promo mail, unless the subject contains “your transaction” or it's receipt mail from billing@example.org"
	if r.Label != want {
		t.Errorf("label = %q\nwant    %q", r.Label, want)
	}
	if len(r.Except) != 2 || *r.Except[0].Subject != "your transaction" || r.Except[0].From != nil ||
		*r.Except[1].From != "billing@example.org" || *r.Except[1].Category != "receipt" || r.Except[1].Subject != nil {
		t.Errorf("except = %+v", r.Except)
	}
	both := RuleSpec{From: "@example.org", Action: "archive", Except: []ExceptSpec{{Category: "important", Subject: "invoice"}}}
	if got := both.label(); got != "Archive mail from example.org, unless it's important mail and the subject contains “invoice”" {
		t.Errorf("label = %q", got)
	}
}

func TestRuleExceptionsProtectMail(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.CreateRule(ctx, RuleSpec{Category: "promo", Action: "trash", Except: []ExceptSpec{
		{Subject: "transaction"},
		{From: "@bank.example.com"},
		{From: "@shop.example.net", Subject: "order"},
	}}); err != nil {
		t.Fatal(err)
	}
	// A later rule still sees mail the first one spared.
	if _, err := f.svc.CreateRule(ctx, RuleSpec{Category: "promo", Action: "mark_read"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Big sale", from: "Shop <deals@shop.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Sale fees on your TRANSACTION", from: "Shop <deals@shop.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Card sale", from: "Bank <offers@mail.bank.example.com>"})
	f.deliver("INBOX", mailSpec{subject: "Sale: your order shipped", from: "Shop <deals@shop.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Sale: your order shipped too", from: "Other <deals@other.example.net>"})
	f.sweep()

	if m := f.bySubject("Big sale"); m.Folder != "Trash" {
		t.Errorf("unexcepted promo: %+v", m)
	}
	for _, sub := range []string{"Sale fees on your TRANSACTION", "Card sale", "Sale: your order shipped"} {
		if m := f.bySubject(sub); m.Folder != "INBOX" || m.Unread {
			t.Errorf("%q should be spared by the trash rule and marked read by the next: %+v", sub, m)
		}
	}
	if m := f.bySubject("Sale: your order shipped too"); m.Folder != "Trash" {
		t.Errorf("an exception needing sender and subject matched on one: %+v", m)
	}
	rules, _ := f.svc.Rules(ctx)
	if rules[0].Hits != 2 || rules[1].Hits != 3 {
		t.Errorf("hits = %d, %d", rules[0].Hits, rules[1].Hits)
	}
}

// Changing a rule's exceptions keeps it (and its hits) and changes what it does
// to new mail.
func TestRuleExceptionsReplaceOnTheSameRule(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	first, err := f.svc.CreateRule(ctx, RuleSpec{From: "@example.org", Action: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Weekly digest"})
	f.sweep()
	updated, err := f.svc.CreateRule(ctx, RuleSpec{From: "@example.org", Action: "archive", Except: []ExceptSpec{{Subject: "urgent"}}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || updated.Hits != 1 || updated.Label != "Archive mail from example.org, unless the subject contains “urgent”" {
		t.Errorf("updated = %+v", updated)
	}
	if again, _ := f.svc.CreateRule(ctx, RuleSpec{From: "@example.org", Action: "archive", Except: []ExceptSpec{{Subject: "Urgent"}}}); again.ID != first.ID {
		t.Error("an identical rule was created twice")
	}
	f.deliver("INBOX", mailSpec{subject: "Urgent: call back"})
	f.deliver("INBOX", mailSpec{subject: "Another digest"})
	f.sweep()
	if m := f.bySubject("Urgent: call back"); m.Folder != "INBOX" {
		t.Errorf("excepted mail moved: %+v", m)
	}
	if m := f.bySubject("Another digest"); m.Folder != "Archive" {
		t.Errorf("rule stopped working: %+v", m)
	}
	// Proposing it without exceptions takes them away again.
	cleared, err := f.svc.CreateRule(ctx, RuleSpec{From: "@example.org", Action: "archive"})
	if err != nil || cleared.ID != first.ID || len(cleared.Except) != 0 || cleared.Label != "Archive mail from example.org" {
		t.Errorf("cleared = %+v, %v", cleared, err)
	}
	if rules, _ := f.svc.Rules(ctx); len(rules) != 1 {
		t.Errorf("rules = %+v", rules)
	}
}

// Without a category, a category exception holds the rule back rather than
// letting it guess.
func TestCategoryExceptionHoldsUnclassifiedMail(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@example.org", Action: "archive", Except: []ExceptSpec{{Category: "important"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@example.net", Action: "archive", Except: []ExceptSpec{{Subject: "keep"}}}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Could be anything"})
	f.deliver("INBOX", mailSpec{subject: "Archive me", from: "N <n@example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Please keep", from: "N <n@example.net>"})
	f.sweep()
	if m := f.bySubject("Could be anything"); m.Folder != "INBOX" {
		t.Errorf("unclassified mail went past a category exception: %+v", m)
	}
	if m := f.bySubject("Archive me"); m.Folder != "Archive" {
		t.Errorf("subject exceptions need no model: %+v", m)
	}
	if m := f.bySubject("Please keep"); m.Folder != "INBOX" {
		t.Errorf("subject exception without a model: %+v", m)
	}
}

// A subject too long for the cache is clipped; words past the cut might be
// there, so a subject exception spares the message.
func TestSubjectExceptionSparesClippedSubjects(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@example.org", Action: "trash", Except: []ExceptSpec{{Subject: "transaction"}}}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("word ", maxCachedSubject/5) + "transaction"
	f.deliver("INBOX", mailSpec{subject: long})
	f.deliver("INBOX", mailSpec{subject: "Short"})
	f.sweep()
	var clipped Message
	for _, m := range f.inbox() {
		if strings.HasPrefix(m.Subject, "word word") {
			clipped = m
		}
	}
	if clipped.ID == "" || strings.Contains(clipped.Subject, "transaction") {
		t.Fatalf("want the long subject clipped before the word and left in the inbox, got %+v", clipped)
	}
	if m := f.bySubject("Short"); m.Folder != "Trash" {
		t.Errorf("short subject: %+v", m)
	}
}
