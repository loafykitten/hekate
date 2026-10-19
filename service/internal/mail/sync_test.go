package mail

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestFirstSync(t *testing.T) {
	f := newFixture(t)
	now := f.clock.Now()
	f.deliver("INBOX", mailSpec{subject: "Re: lunch?", from: "Mina <mina@example.org>"})
	f.deliver("INBOX", mailSpec{subject: "Weekly newsletter", seen: true, headers: "List-Id: <news.example.org>\r\n" +
		"List-Unsubscribe: <mailto:u@example.org>, <https://example.org/unsub?t=1>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"})
	f.deliver("INBOX", mailSpec{subject: "Ancient history", date: now.AddDate(0, 0, -45)})
	f.sweep()

	msgs := f.inbox()
	if len(msgs) != 2 {
		t.Fatalf("cached %d messages, want 2 (the 45-day-old one is outside the window): %+v", len(msgs), msgs)
	}
	reply, news := msgs[0], msgs[1]
	if reply.Subject != "Re: lunch?" || !reply.Unread || reply.Folder != "INBOX" || reply.Account != "personal" ||
		reply.From.Name != "Mina" || reply.From.Address != "mina@example.org" {
		t.Errorf("reply = %+v", reply)
	}
	if str(reply.Category) != "needs_reply" || reply.Gist == nil {
		t.Errorf("reply not classified: %+v", reply)
	}
	if news.Unread || str(news.Category) != "newsletter" || !news.CanUnsubscribe {
		t.Errorf("newsletter = %+v (seen mail must stay read; one-click link expected)", news)
	}
	if reply.CanUnsubscribe {
		t.Error("a message without list headers can't be unsubscribed")
	}
	if f.changeCount() == 0 {
		t.Error("OnChange was not called")
	}
	st, err := f.svc.Status(t.Context())
	if err != nil || len(st) != 1 || !st[0].OK || st[0].InboxCount != 2 || st[0].LastSweepAt == nil || st[0].Address != testUser {
		t.Errorf("status = %+v %v", st, err)
	}
	// Syncing must not have marked anything read on the server.
	uids := f.serverUIDs("INBOX")
	if len(f.serverFlags("INBOX", uids[0])) != 0 {
		t.Errorf("sync changed flags of %v: %v", uids[0], f.serverFlags("INBOX", uids[0]))
	}
}

func TestIncrementalSync(t *testing.T) {
	f := newFixture(t)
	first := f.deliver("INBOX", mailSpec{subject: "First"})
	f.sweep()
	calls := len(f.model.prompts())

	f.deliver("INBOX", mailSpec{subject: "Second sale"})
	f.sweep()
	if got := len(f.inbox()); got != 2 {
		t.Fatalf("cached %d, want 2", got)
	}
	if got := len(f.model.prompts()); got != calls+1 {
		t.Errorf("classification calls = %d, want %d (only the new message)", got, calls+1)
	}
	if c := str(f.bySubject("Second sale").Category); c != "promo" {
		t.Errorf("category = %s", c)
	}

	// The owner reads a message in another app: the flag change is picked up.
	c := f.client()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Store(imapUIDs(first), addSeen(), nil).Close(); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	if f.bySubject("First").Unread {
		t.Error("read state did not follow the server")
	}
}

func TestUIDValidityReset(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Re: keep me", msgID: "keep@example.org"})
	f.deliver("INBOX", mailSpec{subject: "Vanishes", msgID: "vanish@example.org"})
	f.sweep()
	kept := f.bySubject("Re: keep me")
	calls := len(f.model.prompts())

	// The server renumbers the mailbox: delete and recreate INBOX with one of the messages back.
	if err := f.user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	if err := f.user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	f.deliver("INBOX", mailSpec{subject: "Re: keep me", msgID: "keep@example.org"})
	f.deliver("INBOX", mailSpec{subject: "Brand new", msgID: "new@example.org"})
	f.sweep()

	after := f.bySubject("Re: keep me")
	if after.ID != kept.ID || str(after.Category) != "needs_reply" || after.Folder != "INBOX" {
		t.Errorf("row not carried across the reset: before %+v after %+v", kept, after)
	}
	if got := len(f.model.prompts()); got != calls+1 {
		t.Errorf("classification calls = %d, want %d (only the brand-new message)", got, calls+1)
	}
	inbox := f.inbox()
	if len(inbox) != 2 {
		t.Fatalf("inbox = %+v, want the kept and the new message", inbox)
	}
	if v := f.bySubject("Vanishes"); v.Folder != "other" {
		t.Errorf("vanished message = %+v, want it marked as left", v)
	}
	// And the next sweep is a normal incremental one.
	f.deliver("INBOX", mailSpec{subject: "After"})
	f.sweep()
	if len(f.inbox()) != 3 {
		t.Errorf("inbox after the reset = %d, want 3", len(f.inbox()))
	}
}

func TestReconcileMovedElsewhere(t *testing.T) {
	f := newFixture(t)
	uid := f.deliver("INBOX", mailSpec{subject: "Moved away"})
	f.deliver("INBOX", mailSpec{subject: "Stays"})
	f.sweep()
	// Another client moves one to Projects and the sweep sees it leave.
	c := f.client()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Move(imapUIDs(uid), "Projects").Wait(); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	if got := f.inbox(); len(got) != 1 || got[0].Subject != "Stays" {
		t.Errorf("inbox = %+v", got)
	}
	m := f.bySubject("Moved away")
	if m.Folder != "other" {
		t.Errorf("moved message = %+v", m)
	}
	if _, _, err := f.svc.ReadBody(t.Context(), mustID(m.ID)); !errors.Is(err, ErrGone) {
		t.Errorf("reading a message that left: %v", err)
	}
	st, _ := f.svc.Status(t.Context())
	if st[0].InboxCount != 1 {
		t.Errorf("inbox count = %d", st[0].InboxCount)
	}
}

func TestSweepErrorsAreStoredAndRecover(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "Hello"})
	// Wrong password: the account's status shows it, nothing crashes, and the secret isn't in it.
	f.svc.accounts[0].cfg.Pass = "wrong-password"
	f.sweep()
	st, _ := f.svc.Status(t.Context())
	if st[0].OK || st[0].Error == nil || *st[0].Error == "" {
		t.Fatalf("status = %+v, want an error", st[0])
	}
	if strings := *st[0].Error; contains(strings, "wrong-password") || contains(strings, testPass) {
		t.Errorf("error leaks a password: %s", strings)
	}
	f.svc.accounts[0].cfg.Pass = testPass
	f.sweep()
	st, _ = f.svc.Status(t.Context())
	if !st[0].OK || st[0].InboxCount != 1 {
		t.Errorf("status after recovery = %+v", st[0])
	}
}

func TestReconnectAfterConnectionDrops(t *testing.T) {
	f := newFixture(t)
	f.deliver("INBOX", mailSpec{subject: "One"})
	f.sweep()
	// The server side hangs up; after the idle window the next sweep probes and reconnects.
	a := f.svc.accounts[0]
	a.mu.Lock()
	_ = a.nc.Close()
	a.mu.Unlock()
	f.clock.Advance(time.Minute)
	f.deliver("INBOX", mailSpec{subject: "Two"})
	f.sweep()
	if len(f.inbox()) != 2 {
		t.Errorf("inbox = %d, want 2 after reconnecting", len(f.inbox()))
	}
	st, _ := f.svc.Status(t.Context())
	if !st[0].OK {
		t.Errorf("status = %+v", st[0])
	}
}

func TestNoModelStillSyncs(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	f.deliver("INBOX", mailSpec{subject: "Hello"})
	f.sweep()
	m := f.inbox()
	if len(m) != 1 || m[0].Category != nil {
		t.Errorf("inbox = %+v", m)
	}
}

func TestRunSweepsAtStartThenEveryInterval(t *testing.T) {
	f := newFixture(t)
	f.svc.interval = 50 * time.Millisecond
	f.deliver("INBOX", mailSpec{subject: "At start"})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	waitFor(t, func() bool { return len(f.inbox()) == 1 })
	f.deliver("INBOX", mailSpec{subject: "Later"})
	waitFor(t, func() bool { return len(f.inbox()) == 2 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIncrementalSyncCatchesUpOnAFlood(t *testing.T) {
	f := newFixture(t, func(o *fixtureOpts) { o.noModel = true })
	f.deliver("INBOX", mailSpec{subject: "Before"})
	f.sweep()
	for i := range firstSyncCap + 20 {
		f.deliver("INBOX", mailSpec{subject: fmt.Sprintf("Flood %d", i)})
	}
	f.sweep()
	cached := func() (n int) {
		if err := f.store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM mail_messages WHERE folder = 'INBOX'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := cached(); n != 1+firstSyncCap {
		t.Fatalf("after one sweep cached %d, want %d (the oldest new ones first)", n, 1+firstSyncCap)
	}
	f.sweep()
	if n := cached(); n != 1+firstSyncCap+20 {
		t.Errorf("after the next sweep cached %d, want every message", n)
	}
}
