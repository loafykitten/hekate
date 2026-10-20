package mail

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strconv"
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

func TestRuleWordsValidationAndLabel(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	bad := []RuleSpec{
		{Subject: "upgrade", Action: "trash"},                    // words alone don't make a rule
		{Body: "upgrade", Action: "trash"},                       // nor body words
		{From: "@example.org", Subject: "up", Action: "trash"},   // too short to mean anything
		{From: "@example.org", Body: "a\x00bc", Action: "trash"}, // not plain text
		{From: "@example.org", Subject: strings.Repeat("x", 101), Action: "trash"},
	}
	for _, sp := range bad {
		if _, err := f.svc.CreateRule(ctx, sp); err == nil {
			t.Errorf("accepted %+v", sp)
		}
	}
	r, err := f.svc.CreateRule(ctx, RuleSpec{From: "@Example.org", Subject: "  Upgrade\tYour  Plan ", Action: "trash"})
	if err != nil {
		t.Fatal(err)
	}
	if *r.Match.Subject != "upgrade your plan" || r.Match.Body != nil || r.Label != `Trash mail from example.org with "upgrade your plan" in the subject` {
		t.Errorf("rule = %+v", r)
	}
	again, _ := f.svc.CreateRule(ctx, RuleSpec{From: "@example.org", Subject: "upgrade your plan", Action: "trash"})
	if again.ID != r.ID {
		t.Error("an identical rule was created twice")
	}
	plain, _ := f.svc.CreateRule(ctx, RuleSpec{From: "@example.org", Action: "trash"})
	if plain.ID == r.ID || plain.Match.Subject != nil {
		t.Errorf("a rule without words was taken for one with them: %+v", plain)
	}
	both, _ := f.svc.CreateRule(ctx, RuleSpec{Category: "promo", Subject: "sale", Body: "limited time", Action: "move", Folder: "Deals"})
	if both.Label != `Move promo mail with "sale" in the subject and "limited time" in the body to Deals` {
		t.Errorf("label = %q", both.Label)
	}
}

func TestRulesMatchSubjectAndBodyWords(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	ctx := t.Context()
	for _, sp := range []RuleSpec{
		{From: "@bank.example.net", Subject: "upgrade", Action: "trash"},
		{From: "@bank.example.net", Body: "special offer", Action: "archive"},
		{From: "news@example.org", Subject: "weekly", Body: "unsubscribe", Action: "mark_read"},
	} {
		if _, err := f.svc.CreateRule(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}
	bank := "Bank <alerts@bank.example.net>"
	f.deliver("INBOX", mailSpec{subject: "UPGRADE to premium today", from: bank})
	f.deliver("INBOX", mailSpec{subject: "Your statement is ready", from: bank})
	f.deliver("INBOX", mailSpec{subject: "News from us", from: bank, body: "A Special\r\nOffer, just for you."})
	f.deliver("INBOX", mailSpec{subject: "Payment received", from: bank, body: "We received your payment."})
	f.deliver("INBOX", mailSpec{subject: "Upgrade notice", from: "Other <a@other.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "The weekly", from: "News <news@example.org>", body: "Read on. Unsubscribe here."})
	f.deliver("INBOX", mailSpec{subject: "The weekly, again", from: "News <news@example.org>", body: "No footer this time."})
	f.deliver("INBOX", mailSpec{subject: "Monthly", from: "News <news@example.org>", body: "Unsubscribe here."})
	f.sweep()

	want := map[string]struct {
		folder string
		unread bool
	}{
		"UPGRADE to premium today": {"Trash", true},   // subject words, any case
		"Your statement is ready":  {"INBOX", true},   // same sender, no words: kept
		"News from us":             {"Archive", true}, // body words across a line break
		"Payment received":         {"INBOX", true},
		"Upgrade notice":           {"INBOX", true},  // the words, but another sender
		"The weekly":               {"INBOX", false}, // subject and body both match
		"The weekly, again":        {"INBOX", true},  // subject only: not enough
		"Monthly":                  {"INBOX", true},  // body only: not enough
	}
	for subject, w := range want {
		if m := f.bySubject(subject); m.Folder != w.folder || m.Unread != w.unread {
			t.Errorf("%q: folder %s unread %v, want %s %v", subject, m.Folder, m.Unread, w.folder, w.unread)
		}
	}
	rules, _ := f.svc.Rules(ctx)
	for _, r := range rules {
		if r.Hits != 1 {
			t.Errorf("rule %q hits = %d, want 1", r.Label, r.Hits)
		}
	}
}

// Body words are only looked for once the rest of the rule matches, and mail
// whose text can't be read now waits for the next sweep instead of being
// counted as not matching.
func TestBodyRuleWaitsForUnreadableMail(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	ctx := t.Context()
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Body: "flash sale", Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	uid := f.deliver("INBOX", mailSpec{subject: "Vanishes", from: "Shop <deals@shop.example.net>", body: "Flash sale!"})
	a := f.svc.byName["personal"]
	if _, err := f.svc.syncInbox(ctx, a); err != nil {
		t.Fatal(err)
	}
	f.expungeFrom("INBOX", uid) // another mail app deletes it before the rules run
	if n, err := f.svc.applyRules(ctx, a); err != nil || n != 0 {
		t.Fatalf("applyRules = %d, %v", n, err)
	}
	var applied bool
	if err := f.svc.db.QueryRowContext(ctx, `SELECT rules_applied FROM mail_messages WHERE id = ?`, f.id("Vanishes")).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Error("unreadable mail was settled as not matching")
	}
	if got := f.serverUIDs("Trash"); len(got) != 0 {
		t.Errorf("trash = %v", got)
	}
}

func TestRuleProposeToolWords(t *testing.T) {
	f := newFixture(t)
	f.mustCall("mail_rule_propose", `{"from":"@example.net","subject":"Upgrade","action":"trash","reason":"Only the upsell mail."}`)
	r := f.appr.requests()[0]
	var spec RuleSpec
	_ = json.Unmarshal(r.Payload, &spec)
	if r.Title != `Trash mail from example.net with "upgrade" in the subject?` ||
		!reflect.DeepEqual(spec, RuleSpec{From: "@example.net", Subject: "upgrade", Action: "trash"}) {
		t.Errorf("request %+v spec %+v", r, spec)
	}
	if _, err := f.appr.run(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if rules, _ := f.svc.Rules(t.Context()); len(rules) != 1 || rules[0].Match.Subject == nil || *rules[0].Match.Subject != "upgrade" {
		t.Errorf("rules = %+v", rules)
	}
	if _, err := f.call("mail_rule_propose", `{"subject":"upgrade","action":"trash","reason":"x"}`); err == nil {
		t.Error("a rule of words alone was proposed")
	}
}

func TestRuleExceptionsSpareMatchingMail(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@bank.example.net", Action: "trash", Except: []RuleException{
		{Category: "receipt"},
		{Subject: "Transaction"},
	}}); err != nil {
		t.Fatal(err)
	}
	// A later rule still gets mail the first one spares.
	if _, err := f.svc.CreateRule(ctx, RuleSpec{Category: "receipt", Action: "mark_read"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Big sale on upgrades", from: "Bank <news@bank.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Your receipt", from: "Bank <billing@bank.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "New transaction on your card", from: "Bank <alerts@bank.example.net>"})
	f.sweep()

	if m := f.bySubject("Big sale on upgrades"); m.Folder != "Trash" {
		t.Errorf("unexcepted mail spared: %+v", m)
	}
	if m := f.bySubject("Your receipt"); m.Folder != "INBOX" || m.Unread {
		t.Errorf("category exception: want spared by the trash rule, then marked read by the next: %+v", m)
	}
	if m := f.bySubject("New transaction on your card"); m.Folder != "INBOX" || !m.Unread {
		t.Errorf("subject exception: %+v", m)
	}
}

func TestRuleExceptionsFailSafe(t *testing.T) {
	ex := Rule{Except: []RuleException{{Category: "receipt"}, {Subject: "transaction"}}}
	unclassified := row{FromAddr: "a@example.net", Subject: "Hello"}
	if !ex.spares(unclassified) {
		t.Error("mail with no category should count as matching a category exception")
	}
	promo := row{FromAddr: "a@example.net", Subject: "Hello", Category: sql.NullString{String: "promo", Valid: true}}
	if ex.spares(promo) {
		t.Error("a promo spared by a receipt exception")
	}
	clipped := promo
	clipped.Subject = strings.Repeat("x", maxCachedSubject) + "…"
	if !ex.spares(clipped) {
		t.Error("a clipped subject should count as containing the exception's words")
	}
	// All of an exception's fields must match.
	both := Rule{Except: []RuleException{{From: "@bank.example.net", Subject: "transaction"}}}
	if both.spares(row{FromAddr: "a@other.example.net", Subject: "transaction", Category: promo.Category}) {
		t.Error("an exception matched on one of its two fields")
	}
}

func TestRuleExceptionsValidateAndLabel(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for name, ex := range map[string][]RuleException{
		"empty":    {{}},
		"category": {{Category: "spam"}},
		"sender":   {{From: "not an address"}},
		"short":    {{Subject: "ab"}},
		"too many": {{Subject: "aaa"}, {Subject: "bbb"}, {Subject: "ccc"}, {Subject: "ddd"}, {Subject: "eee"}, {Subject: "fff"}},
	} {
		if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@example.net", Action: "trash", Except: ex}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r, err := f.svc.CreateRule(ctx, RuleSpec{Category: "promo", From: "@Example.net", Action: "trash", Except: []RuleException{
		{Subject: "  Transaction "}, {Category: "Receipt"}, {Subject: "transaction"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "Trash promo mail from example.net, except mail with “transaction” in the subject or receipt mail"
	if r.Label != want {
		t.Errorf("label = %q, want %q", r.Label, want)
	}
	if len(r.Except) != 2 {
		t.Errorf("exceptions = %+v, want the repeat dropped", r.Except)
	}
	// The same exceptions in another order are the same rule.
	again, err := f.svc.CreateRule(ctx, RuleSpec{Category: "promo", From: "@example.net", Action: "trash", Except: []RuleException{
		{Category: "receipt"}, {Subject: "TRANSACTION"},
	}})
	if err != nil || again.ID != r.ID {
		t.Errorf("same rule = %+v, %v; want id %s", again, err, r.ID)
	}
}

func TestNewExceptionsReplaceTheRuleInPlace(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	first, _ := f.svc.CreateRule(ctx, RuleSpec{From: "@bank.example.net", Action: "trash"})
	second, _ := f.svc.CreateRule(ctx, RuleSpec{Category: "promo", Action: "archive"})
	if _, err := f.store.DB().Exec(`UPDATE mail_rules SET hits = 7 WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	r, replaced, err := f.svc.putRule(ctx, RuleSpec{From: "@bank.example.net", Action: "trash", Except: []RuleException{{Category: "receipt"}}})
	if err != nil || !replaced {
		t.Fatalf("put = %+v, %v, %v", r, replaced, err)
	}
	if r.ID == first.ID || r.Hits != 7 || r.CreatedAt != first.CreatedAt || len(r.Except) != 1 {
		t.Errorf("replacement = %+v", r)
	}
	rules, _ := f.svc.Rules(ctx)
	if len(rules) != 2 || rules[0].ID != r.ID || rules[1].ID != second.ID {
		t.Errorf("rules = %+v; want the replacement first, where the original was", rules)
	}
	// An in-flight sweep sees the old rule as deleted.
	if id, _ := strconv.ParseInt(first.ID, 10, 64); f.svc.ruleExists(ctx, id) {
		t.Error("the replaced rule still exists")
	}
}

func TestRuleProposeToolExceptions(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@bank.example.net", Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	f.mustCall("mail_rule_propose", `{"from":"@bank.example.net","action":"trash","except":[{"category":"receipt"}],"reason":"Keep the receipts."}`)
	r := f.appr.requests()[0]
	if r.Title != "Update the rule: Trash mail from bank.example.net, except receipt mail?" ||
		!strings.HasPrefix(r.Detail, "Replaces “Trash mail from bank.example.net”.") {
		t.Errorf("request = %+v", r)
	}
	if _, err := f.appr.run(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	res := f.mustCall("mail_rules", `{}`).Content
	if !strings.Contains(res, "except receipt mail") || strings.Count(res, "\n") != 0 {
		t.Errorf("mail_rules = %q", res)
	}
	// Proposing it again changes nothing and asks nobody.
	again := f.mustCall("mail_rule_propose", `{"from":"@bank.example.net","action":"trash","except":[{"category":"receipt"}],"reason":"x"}`).Content
	if !strings.Contains(again, "already exists") || len(f.appr.requests()) != 1 {
		t.Errorf("again = %q, %d requests", again, len(f.appr.requests()))
	}
}

func TestSparedMailNeedsNoBody(t *testing.T) {
	r := Rule{Match: RuleMatch{From: ptr("@bank.example.net"), Body: ptr("upgrade")}, Except: []RuleException{{Subject: "transaction"}}}
	spared := row{FromAddr: "a@bank.example.net", Subject: "New transaction"}
	if r.needsBody(spared) {
		t.Error("a message an exception spares made the rule read its body")
	}
	other := row{FromAddr: "a@bank.example.net", Subject: "News"}
	if !r.needsBody(other) {
		t.Error("an unspared match should still read the body")
	}
}
