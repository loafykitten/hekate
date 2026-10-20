package mail

import (
	"strings"
	"testing"
	"time"
)

func TestUnsubscribeRuleValidation(t *testing.T) {
	f := newFixture(t)
	bad := []RuleSpec{
		{Category: "promo", Action: "unsubscribe"},                      // no sender: a category alone never picks where requests go
		{From: "@shop.example.net", Action: "unsubscribe", Folder: "X"}, // folder without move
	}
	for _, sp := range bad {
		if _, err := f.svc.CreateRule(t.Context(), sp); err == nil {
			t.Errorf("accepted %+v", sp)
		}
	}
	r, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@Shop.example.net", Action: " Unsubscribe "})
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != "unsubscribe" || r.Label != "Unsubscribe from shop.example.net" {
		t.Errorf("rule = %+v", r)
	}
	if l := (RuleSpec{From: "deals@shop.example.net", Category: "promo", Action: "unsubscribe"}).label(); l != "Unsubscribe from deals@shop.example.net (promo mail)" {
		t.Errorf("label = %q", l)
	}
}

func listMail(url, list string) string {
	return oneClickHeaders(url) + "List-Id: <" + list + ">\r\n"
}

func ruleHits(t *testing.T, f *fixture) map[string]int {
	t.Helper()
	rules, err := f.svc.Rules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	hits := map[string]int{}
	for _, r := range rules {
		hits[r.Action] = r.Hits
		if r.Hits > 0 && r.LastHitAt == nil {
			t.Errorf("rule %s has hits but no last_hit_at", r.ID)
		}
	}
	return hits
}

func TestUnsubscribeRuleSendsOneClickAndFilesNothing(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	shop := "Shop <deals@shop.example.net>"
	f.deliver("INBOX", mailSpec{subject: "Sale", from: shop, headers: listMail(srv.URL+"/u/sale", "deals.shop.example.net")})
	f.deliver("INBOX", mailSpec{subject: "Order shipped", from: "Shop <orders@shop.example.net>"}) // no one-click: left alone
	f.deliver("INBOX", mailSpec{subject: "Other list", headers: listMail(srv.URL+"/u/other", "news.example.org")})
	f.sweep()

	if got := srv.reqs(); len(got) != 1 || got[0] != "POST /u/sale List-Unsubscribe=One-Click application/x-www-form-urlencoded" {
		t.Errorf("requests = %q", got)
	}
	if m := f.bySubject("Sale"); m.Folder != "INBOX" || m.CanUnsubscribe {
		t.Errorf("sale = %+v, want it unsubscribed and left in the inbox", m)
	}
	if !f.bySubject("Other list").CanUnsubscribe {
		t.Error("a sender no rule names was unsubscribed")
	}
	if h := ruleHits(t, f); h["unsubscribe"] != 1 {
		t.Errorf("hits = %v", h)
	}

	// More mail on the same list within the week sends nothing more; a
	// different list from the same domain is its own request.
	f.deliver("INBOX", mailSpec{subject: "Sale 2", from: shop, headers: listMail(srv.URL+"/u/sale2", "deals.shop.example.net")})
	f.deliver("INBOX", mailSpec{subject: "Digest", from: "Shop <digest@shop.example.net>", headers: listMail(srv.URL+"/u/digest", "digest.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 2 || !strings.HasPrefix(got[1], "POST /u/digest ") {
		t.Errorf("requests = %q", got)
	}
	if !f.bySubject("Sale 2").CanUnsubscribe {
		t.Error("asked the same list twice in a week")
	}

	// A sender still mailing a week later is asked again.
	f.clock.Advance(unsubEvery + time.Hour)
	f.deliver("INBOX", mailSpec{subject: "Sale 3", from: shop, headers: listMail(srv.URL+"/u/sale3", "deals.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 3 || !strings.HasPrefix(got[2], "POST /u/sale3 ") {
		t.Errorf("requests = %q", got)
	}
	if h := ruleHits(t, f); h["unsubscribe"] != 3 {
		t.Errorf("hits = %v", h)
	}
}

func TestUnsubscribeRulePairsWithFilingRule(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	for _, sp := range []RuleSpec{
		{From: "@shop.example.net", Action: "unsubscribe"},
		{From: "@shop.example.net", Action: "trash"},
	} {
		if _, err := f.svc.CreateRule(t.Context(), sp); err != nil {
			t.Fatal(err)
		}
	}
	f.deliver("INBOX", mailSpec{subject: "Sale", from: "Shop <deals@shop.example.net>", headers: listMail(srv.URL+"/u/1", "deals.shop.example.net")})
	f.sweep()
	if m := f.bySubject("Sale"); m.Folder != "Trash" || m.CanUnsubscribe {
		t.Errorf("message = %+v, want it trashed and unsubscribed", m)
	}
	if len(srv.reqs()) != 1 {
		t.Errorf("requests = %q", srv.reqs())
	}
	if h := ruleHits(t, f); h["unsubscribe"] != 1 || h["trash"] != 1 {
		t.Errorf("hits = %v", h)
	}
}

func TestUnsubscribeRuleTriesEachMessageOnce(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	shop := "Shop <deals@shop.example.net>"
	srv.status = 500
	f.deliver("INBOX", mailSpec{subject: "Sale", from: shop, headers: listMail(srv.URL+"/u/1", "deals.shop.example.net")})
	f.sweep()
	f.sweep()
	if got := srv.reqs(); len(got) != 1 {
		t.Errorf("a failed request was retried for the same message: %q", got)
	}
	if !f.bySubject("Sale").CanUnsubscribe {
		t.Error("a failed request counted as done")
	}
	if h := ruleHits(t, f); h["unsubscribe"] != 0 {
		t.Errorf("hits = %v", h)
	}
	// The sender's next message tries again.
	srv.status = 200
	f.deliver("INBOX", mailSpec{subject: "Sale 2", from: shop, headers: listMail(srv.URL+"/u/2", "deals.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 2 || !strings.HasPrefix(got[1], "POST /u/2 ") {
		t.Errorf("requests = %q", got)
	}
}

// A filing rule that fails leaves its mail for the next sweep; the unsubscribe
// waits with it rather than going out once now and again on the retry.
func TestUnsubscribeRuleWaitsForFailedFilingRule(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	for _, sp := range []RuleSpec{
		{From: "@shop.example.net", Action: "unsubscribe"},
		{From: "@shop.example.net", Action: "move", Folder: "Later"}, // doesn't exist yet
	} {
		if _, err := f.svc.CreateRule(t.Context(), sp); err != nil {
			t.Fatal(err)
		}
	}
	srv.status = 500
	f.deliver("INBOX", mailSpec{subject: "Sale", from: "Shop <deals@shop.example.net>", headers: listMail(srv.URL+"/u/1", "deals.shop.example.net")})
	f.sweep()
	f.sweep()
	if got := srv.reqs(); len(got) != 0 {
		t.Errorf("unsubscribed before the mail was settled: %q", got)
	}
	if err := f.user.Create("Later", nil); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	f.sweep()
	if m := f.bySubject("Sale"); m.Folder != "Later" {
		t.Errorf("message = %+v", m)
	}
	if got := srv.reqs(); len(got) != 1 {
		t.Errorf("requests = %q, want one try once the mail settled", got)
	}
}
