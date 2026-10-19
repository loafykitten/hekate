package mail

import "testing"

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
