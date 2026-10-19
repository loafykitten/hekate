package mail

import (
	"testing"

	"github.com/loafykitten/hekate/service/internal/config"
)

// Pointing an account label at a different mailbox must not let old ids (and
// approvals that hold them) reach messages there, even when the UIDs overlap.
func TestAccountPointedElsewhereStartsFresh(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "From the old mailbox"})
	f.sweep()
	old := f.id("From the old mailbox")

	same := []config.MailAccount{{Name: "personal", User: testUser, Pass: testPass, Host: f.addr, Archive: "Archive"}}
	if _, err := New(Options{Store: f.store, Accounts: same, Approvals: &fakeApprovals{t: t, store: f.store, clock: f.clock}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Message(t.Context(), old); err != nil {
		t.Fatalf("a restart with the same mailbox must keep the cache: %v", err)
	}

	moved := []config.MailAccount{{Name: "personal", User: "someone-else@example.org", Pass: testPass, Host: f.addr, Archive: "Archive"}}
	svc, err := New(Options{Store: f.store, Accounts: moved, Approvals: &fakeApprovals{t: t, store: f.store, clock: f.clock}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Message(t.Context(), old); err == nil {
		t.Error("an old message id still resolves after the account moved to another mailbox")
	}
	if _, err := svc.Trash(t.Context(), []int64{old}); err == nil {
		t.Error("an old id could still be acted on")
	}
	st, err := svc.accountState(t.Context(), "personal")
	if err != nil || st.validity != 0 || st.lastUID != 0 || st.sweepAt.Valid {
		t.Errorf("account state after the move = %+v, %v (want a fresh first sync)", st, err)
	}
}
