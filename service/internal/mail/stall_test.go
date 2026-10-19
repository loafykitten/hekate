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
