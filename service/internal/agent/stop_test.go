package agent

import (
	"context"
	"errors"
	"testing"
)

// A written turn is stopped without a heard boundary, so a piece the emitter
// sends while it stops is still kept; a spoken one keeps what was delivered.
func TestStopBoundary(t *testing.T) {
	for _, written := range []bool{true, false} {
		ctx, cancel := context.WithCancelCause(context.Background())
		tr := &turn{id: "t", ctx: ctx, cancel: cancel, done: make(chan struct{})}
		tr.heard.Store(heardUnset)
		tr.lastSeq.Store(3)
		a := &Agent{written: written, cur: tr}
		a.Stop()
		if !errors.Is(context.Cause(ctx), errInterrupted) {
			t.Errorf("written=%v: not cancelled as interrupted: %v", written, context.Cause(ctx))
		}
		want := int64(3)
		if written {
			want = heardUnset
		}
		if got := tr.heard.Load(); got != want {
			t.Errorf("written=%v: heard boundary %d, want %d", written, got, want)
		}
	}
	// A finished turn is left alone.
	ctx, cancel := context.WithCancelCause(context.Background())
	tr := &turn{id: "t", ctx: ctx, cancel: cancel, done: make(chan struct{}), fin: &finished{}}
	(&Agent{written: true, cur: tr}).Stop()
	if ctx.Err() != nil {
		t.Error("stopped a finished turn")
	}
}
