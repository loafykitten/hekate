package mail

import (
	"context"
	"net"
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

func TestRemovedOrRenamedAccountLeavesNoStaleMail(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Cached under the old label"})
	f.sweep()

	renamed := []config.MailAccount{{Name: "home", User: testUser, Pass: testPass, Host: f.addr, Archive: "Archive"}}
	svc, err := New(Options{Store: f.store, Accounts: renamed, Approvals: &fakeApprovals{t: t, store: f.store, clock: f.clock},
		Now: f.clock.Now, Dial: func(ctx context.Context, a config.MailAccount) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", a.Host)
		}})
	if err != nil {
		t.Fatal(err)
	}
	if msgs, _ := svc.Messages(t.Context(), Query{AllFolders: true, Limit: 50}); len(msgs) != 0 {
		t.Errorf("messages after a rename = %+v, want the old label's copies gone", msgs)
	}
	svc.Sweep(t.Context())
	if msgs, _ := svc.Messages(t.Context(), Query{Limit: 50}); len(msgs) != 1 || msgs[0].Account != "home" {
		t.Errorf("after the first sweep under the new label = %+v", msgs)
	}

	// Mail switched off entirely: nothing stale remains either.
	if _, err := New(Options{Store: f.store}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM mail_messages`).Scan(&n); err != nil || n != 0 {
		t.Errorf("cached rows with no accounts = %d, %v", n, err)
	}
}
