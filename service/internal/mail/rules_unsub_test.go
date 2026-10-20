package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
)

func TestUnsubscribeRuleValidation(t *testing.T) {
	f := newFixture(t)
	bad := []RuleSpec{
		{Category: "promo", Action: "unsubscribe"},                            // no sender: a category alone never picks where requests go
		{From: "@shop.example.net", Category: "promo", Action: "unsubscribe"}, // categories aren't vouched for by the signature
		{From: "@shop.example.net", Action: "unsubscribe", Except: []RuleException{{Category: "receipt"}}},
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
	if l := (RuleSpec{From: "deals@shop.example.net", Action: "unsubscribe"}).label(); l != "Unsubscribe from deals@shop.example.net" {
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
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale", from: shop, headers: listMail(srv.URL+"/u/sale", "deals.shop.example.net")})
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
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale 2", from: shop, headers: listMail(srv.URL+"/u/sale2", "deals.shop.example.net")})
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Digest", from: "Shop <digest@shop.example.net>", headers: listMail(srv.URL+"/u/digest", "digest.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 2 || !strings.HasPrefix(got[1], "POST /u/digest ") {
		t.Errorf("requests = %q", got)
	}
	if !f.bySubject("Sale 2").CanUnsubscribe {
		t.Error("asked the same list twice in a week")
	}

	// A sender still mailing a week later is asked again.
	f.clock.Advance(unsubEvery + time.Hour)
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale 3", from: shop, headers: listMail(srv.URL+"/u/sale3", "deals.shop.example.net")})
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
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale", from: "Shop <deals@shop.example.net>", headers: listMail(srv.URL+"/u/1", "deals.shop.example.net")})
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
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale", from: shop, headers: listMail(srv.URL+"/u/1", "deals.shop.example.net")})
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
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale 2", from: shop, headers: listMail(srv.URL+"/u/2", "deals.shop.example.net")})
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
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale", from: "Shop <deals@shop.example.net>", headers: listMail(srv.URL+"/u/1", "deals.shop.example.net")})
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

// Each inbox's subscription is its own: the same list mailing two accounts gets
// a request from each, even within the week.
func TestUnsubscribeRuleLimitIsPerInbox(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) {
		o.httpc = srv.Client()
		o.accounts = []config.MailAccount{
			{Name: "home", User: testUser, Pass: testPass, Archive: "Archive"},
			{Name: "work", User: testUser, Pass: testPass, Archive: "Archive"}, // same mailbox, so both see the mail
		}
	})
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Sale", from: "Shop <deals@shop.example.net>", headers: listMail(srv.URL+"/u/sale", "deals.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 2 {
		t.Errorf("requests = %q, want one from each inbox", got)
	}
	if h := ruleHits(t, f); h["unsubscribe"] != 2 {
		t.Errorf("hits = %v", h)
	}
}

func TestUnsubscribeRuleWithExceptionsAndWords(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Body: "sale", Action: "unsubscribe"}); err == nil {
		t.Error("an unsubscribe rule took body words")
	}
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe",
		Except: []RuleException{{Subject: "your order"}}}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "News about your order", from: "Shop <news@shop.example.net>", headers: listMail(srv.URL+"/u/order", "orders.shop.example.net")})
	f.deliver("INBOX", mailSpec{dkim: "shop.example.net", subject: "Weekly deals", from: "Shop <deals@shop.example.net>", headers: listMail(srv.URL+"/u/deals", "deals.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 1 || !strings.HasPrefix(got[0], "POST /u/deals ") {
		t.Errorf("requests = %q, want only the unspared list", got)
	}
}

// Only mail whose unsubscribe headers the sender's own domain signed is acted
// on: anyone can forge a From line and point List-Unsubscribe anywhere.
func TestUnsubscribeRuleNeedsTheSendersSignature(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []mailSpec{
		{subject: "Unsigned", headers: listMail(srv.URL+"/u/unsigned", "a.shop.example.net")},
		{subject: "Signed by someone else", dkim: "attacker.example.org", headers: listMail(srv.URL+"/u/other", "b.shop.example.net")},
		{subject: "Headers not signed", dkim: "shop.example.net", dkimKeys: []string{"From", "Subject"}, headers: listMail(srv.URL+"/u/partial", "c.shop.example.net")},
		{subject: "Signed by a subdomain", dkim: "eu.shop.example.net", headers: listMail(srv.URL+"/u/sub", "d.shop.example.net")},
	} {
		m.from = "Shop <deals@shop.example.net>"
		f.deliver("INBOX", m)
	}
	// A genuinely signed message with a second List-Unsubscribe added on top: the
	// signature covers the bottom copy, the cache would read the top one.
	f.deliver("INBOX", mailSpec{subject: "Replayed", from: "Shop <deals@shop.example.net>", dkim: "shop.example.net",
		headers: listMail(srv.URL+"/u/signed", "f.shop.example.net"), forged: "List-Unsubscribe: <" + srv.URL + "/u/evil>\r\n"})
	// The subject decides exceptions, so it must be signed, and only once.
	f.deliver("INBOX", mailSpec{subject: "Deals", from: "Shop <deals@shop.example.net>", dkim: "shop.example.net",
		dkimKeys: []string{"From", "List-Unsubscribe", "List-Unsubscribe-Post"}, headers: listMail(srv.URL+"/u/nosubject", "g.shop.example.net")})
	// A sender on a subdomain, signed by the parent domain, is the sender's own.
	f.deliver("INBOX", mailSpec{subject: "Genuine", from: "Shop <deals@mail.shop.example.net>", dkim: "shop.example.net",
		headers: listMail(srv.URL+"/u/genuine", "e.shop.example.net")})
	f.sweep()
	if got := srv.reqs(); len(got) != 1 || !strings.HasPrefix(got[0], "POST /u/genuine ") {
		t.Errorf("requests = %q, want only the genuine one", got)
	}
}

// A sweep cut short after the mail was settled still sends the request on the
// next sweep, once.
func TestUnsubscribeDueSurvivesAnInterruptedSweep(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	r, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe"})
	if err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Sale", from: "Shop <deals@shop.example.net>", dkim: "shop.example.net",
		headers: listMail(srv.URL+"/u/sale", "deals.shop.example.net")})
	f.sweep()
	if len(srv.reqs()) != 1 {
		t.Fatalf("requests = %q", srv.reqs())
	}
	// What an interrupted sweep leaves: settled mail still marked due.
	f.deliver("INBOX", mailSpec{subject: "Digest", from: "Shop <digest@shop.example.net>", dkim: "shop.example.net",
		headers: listMail(srv.URL+"/u/digest", "digest.shop.example.net")})
	f.sweep()
	db := f.store.DB()
	if _, err := db.Exec(`UPDATE mail_messages SET unsubscribed_at = NULL, unsub_rule = ? WHERE subject = 'Digest'`, r.ID); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	f.sweep()
	if got := srv.reqs(); len(got) != 3 || !strings.HasPrefix(got[2], "POST /u/digest ") {
		t.Errorf("requests = %q, want the due one sent once more", got)
	}
	var left int
	_ = db.QueryRow(`SELECT count(*) FROM mail_messages WHERE unsub_rule IS NOT NULL`).Scan(&left)
	if left != 0 {
		t.Errorf("%d messages still marked due", left)
	}
}

// A replayed signed message can't dodge an exception by putting another
// subject on top.
func TestUnsubscribeExceptionsCantBeDodgedByASecondSubject(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe",
		Except: []RuleException{{Subject: "your order"}}}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "About your order", from: "Shop <orders@shop.example.net>", dkim: "shop.example.net",
		headers: listMail(srv.URL+"/u/orders", "orders.shop.example.net"), forged: "Subject: Big sale\r\n"})
	f.sweep()
	if got := srv.reqs(); len(got) != 0 {
		t.Errorf("requests = %q, want none", got)
	}
}

// The weekly limit goes by the signed sender address: a replay with another
// List-Id on top gets no second request.
func TestUnsubscribeLimitIgnoresUnsignedListID(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	if _, err := f.svc.CreateRule(t.Context(), RuleSpec{From: "@shop.example.net", Action: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	spec := mailSpec{subject: "Sale", from: "Shop <deals@shop.example.net>", dkim: "shop.example.net",
		headers: listMail(srv.URL+"/u/sale", "deals.shop.example.net")}
	f.deliver("INBOX", spec)
	f.sweep()
	spec.forged = "List-Id: <another.example.org>\r\n"
	f.deliver("INBOX", spec)
	f.sweep()
	if got := srv.reqs(); len(got) != 1 {
		t.Errorf("requests = %q, want one", got)
	}
}

// A cached row is only acted on with the headers of the message whose signature
// was checked: a row left holding another copy's link must not ride on it.
func TestUnsubscribeCacheMustMatchTheSignedMessage(t *testing.T) {
	raw := mailSpec{subject: "Big =?utf-8?q?sale?=", from: "Shop <Deals@shop.example.net>", dkim: "shop.example.net",
		headers: listMail("https://shop.example.net/u/1", "deals.shop.example.net")}.raw(time.Now())
	m := row{FromAddr: "deals@shop.example.net", Subject: "Big sale", UnsubURL: "https://shop.example.net/u/1", HasPost: true}
	if !cacheMatches(raw, m) {
		t.Fatal("the matching row was refused")
	}
	for name, change := range map[string]func(*row){
		"link":    func(r *row) { r.UnsubURL = "https://evil.example.org/u" },
		"sender":  func(r *row) { r.FromAddr = "other@shop.example.net" },
		"subject": func(r *row) { r.Subject = "Your order" },
		"post":    func(r *row) { r.HasPost = false },
	} {
		bad := m
		change(&bad)
		if cacheMatches(raw, bad) {
			t.Errorf("%s: a row with another message's headers was accepted", name)
		}
	}
}
