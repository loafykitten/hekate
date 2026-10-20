package mail

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestStalledServerIsCutOff(t *testing.T) {
	var stall atomic.Bool
	f := newFixture(t, func(o *fixtureOpts) { o.stall, o.noMove = &stall, true }) // the wrapper hides the session's MOVE
	f.deliver("INBOX", mailSpec{subject: "Hello"})
	f.svc.opTimeout = 200 * time.Millisecond
	stall.Store(true)
	t.Cleanup(func() { stall.Store(false) })

	began := time.Now()
	f.sweep()
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("sweep took %v against a stalled server", took)
	}
	st, err := f.svc.Status(t.Context())
	if err != nil || len(st) != 1 || st[0].OK || st[0].Error == nil {
		t.Fatalf("status after a stall = %+v, %v", st, err)
	}

	// Once the server answers again, the next sweep reconnects and catches up.
	stall.Store(false)
	f.sweep()
	if got := f.inbox(); len(got) != 1 || got[0].Subject != "Hello" {
		t.Errorf("inbox after recovery = %+v", got)
	}
}

func TestRecoveryAloneRefreshesClients(t *testing.T) {
	var stall atomic.Bool
	f := newFixture(t, func(o *fixtureOpts) { o.stall, o.noMove = &stall, true })
	f.deliver("INBOX", mailSpec{subject: "Hello"})
	f.svc.opTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stall.Store(false) })
	f.sweep()
	stall.Store(true)
	f.sweep()
	if st, _ := f.svc.Status(t.Context()); st[0].OK {
		t.Fatalf("status during a stall = %+v", st[0])
	}

	stall.Store(false)
	before := f.changeCount()
	f.sweep() // nothing new arrived: only the error clears
	if got := f.changeCount() - before; got != 1 {
		t.Errorf("OnChange calls after recovering = %d, want 1 so an open Mail screen drops the error", got)
	}
	if st, _ := f.svc.Status(t.Context()); !st[0].OK {
		t.Errorf("status after recovery = %+v", st[0])
	}
}
