package mail

import (
	"strconv"
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
