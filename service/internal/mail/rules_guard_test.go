package mail

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A rule selects mail before it takes the account's lock; if the owner files a
// message in between, the rule must leave it where they put it.
func TestRuleSkipsMailFiledSinceItWasSelected(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Filed by hand"})
	f.sweep()
	id := f.id("Filed by hand")
	if _, err := f.svc.Archive(t.Context(), []int64{id}); err != nil { // the owner, while the rule waits
		t.Fatal(err)
	}

	out, err := f.svc.perform(t.Context(), ActionTrash, "", []int64{id}, unruledInbox) // the rule, with its stale selection
	if err != nil || out.Done != 0 {
		t.Fatalf("rule on filed mail = %+v, %v", out, err)
	}
	if m, _ := f.svc.Message(t.Context(), id); m.Folder != "Archive" {
		t.Errorf("message moved to %q, want it left in Archive", m.Folder)
	}
	if got := f.serverUIDs("Trash"); len(got) != 0 {
		t.Errorf("trash = %v", got)
	}
}

// A rule deleted while its action waits for the account must not run.
func TestDeletedRuleDoesNotRunWhileQueued(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	ctx := t.Context()
	rule, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Action: "trash"})
	if err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Sale", from: "Shop <deals@shop.example.net>"})
	a := f.svc.byName["personal"]
	if _, err := f.svc.syncInbox(ctx, a); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock() // something else holds the connection
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := f.svc.applyRules(ctx, a); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // the rule has picked its mail and waits for the lock
	id, _ := strconv.ParseInt(rule.ID, 10, 64)
	if err := f.svc.DeleteRule(ctx, id); err != nil {
		t.Fatal(err)
	}
	a.mu.Unlock()
	<-done

	if got := f.serverUIDs("Trash"); len(got) != 0 {
		t.Errorf("a deleted rule trashed mail: %v", got)
	}
	if m := f.bySubject("Sale"); m.Folder != "INBOX" {
		t.Errorf("message = %+v, want it left in the inbox", m)
	}
}

// An exception added while a rule's action waits for the account protects the
// mail it covers, and leaves that mail for the other rules.
func TestExceptionAddedWhileQueuedProtectsMail(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	ctx := t.Context()
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Your transaction", from: "Shop <deals@shop.example.net>"})
	f.deliver("INBOX", mailSpec{subject: "Sale", from: "Shop <deals@shop.example.net>"})
	a := f.svc.byName["personal"]
	if _, err := f.svc.syncInbox(ctx, a); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := f.svc.applyRules(ctx, a); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // the rule has picked its mail and waits for the lock
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Action: "trash", Except: []ExceptSpec{{Subject: "transaction"}}}); err != nil {
		t.Fatal(err)
	}
	a.mu.Unlock()
	<-done

	if m := f.bySubject("Your transaction"); m.Folder != "INBOX" {
		t.Errorf("message = %+v, want it left in the inbox", m)
	}
	if m := f.bySubject("Sale"); m.Folder != "Trash" {
		t.Errorf("message = %+v, want it trashed", m)
	}
	if r, _ := f.svc.rowByID(ctx, f.id("Your transaction")); r.RulesApplied {
		t.Error("spared mail was settled before the other rules saw it")
	}
}

// A rule change approved while the rule's action is under way (checked, but
// not yet done on the server) waits for that action rather than racing it.
func TestRuleChangeWaitsForAnActionUnderWay(t *testing.T) {
	var stall atomic.Bool
	f := newFixture(t, func(o *fixtureOpts) { o.noModel, o.stall, o.noMove = true, &stall, true }) // the wrapper hides MOVE
	ctx := t.Context()
	if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Action: "mark_read"}); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Your transaction", from: "Shop <deals@shop.example.net>"})
	a := f.svc.byName["personal"]
	if _, err := f.svc.syncInbox(ctx, a); err != nil {
		t.Fatal(err)
	}

	stall.Store(true) // the action passes its check, then hangs selecting the inbox to mark it
	t.Cleanup(func() { stall.Store(false) })
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		if _, err := f.svc.applyRules(ctx, a); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	changed := make(chan struct{})
	go func() {
		defer close(changed)
		if _, err := f.svc.CreateRule(ctx, RuleSpec{From: "@shop.example.net", Action: "mark_read", Except: []ExceptSpec{{Subject: "transaction"}}}); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-changed:
		t.Fatal("the rule changed while its action was under way")
	case <-time.After(200 * time.Millisecond):
	}
	stall.Store(false)
	<-swept
	<-changed
	if m := f.bySubject("Your transaction"); m.Unread {
		t.Errorf("message = %+v, want the action that was under way finished", m)
	}
	if rules, _ := f.svc.Rules(ctx); len(rules) != 1 || len(rules[0].Except) != 1 {
		t.Errorf("rules = %+v", rules)
	}
}
