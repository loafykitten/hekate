package lab

import (
	"context"
	"strings"
)

// Run keeps on-demand security checks going until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.bgMu.Lock()
	s.bgCtx = ctx
	s.bgMu.Unlock()
	<-ctx.Done()
	s.bg.Wait()
}

// goBackground runs f under Run's context; false when Run isn't going.
func (s *Service) goBackground(f func(ctx context.Context)) bool {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if s.bgCtx == nil || s.bgCtx.Err() != nil {
		return false
	}
	ctx := s.bgCtx
	s.bg.Go(func() { f(ctx) })
	return true
}

// guardedSnapshot takes a snapshot only when it can't cost the last one a
// security check vouched for: the newest snapshot must be a nightly one (taken
// after a complete, clean check), and the last check complete and free of any
// sign of trouble. With two kept, a second one before the next night goes
// without. It says why when it takes none.
func (s *Service) guardedSnapshot(ctx context.Context, label string) (string, string, error) {
	// Held from the checks through the snapshot: two threads asking at once
	// mustn't both see the checked snapshot as safe to keep.
	s.snapping.Lock()
	defer s.snapping.Unlock()
	c, err := s.lastCheck(ctx)
	if err != nil {
		return "", "", err
	}
	switch {
	case c == nil:
		return "", "no security check has vouched for the lab yet", nil
	case !c.Scanned:
		return "", "the last security check didn't see everything", nil
	case rank[c.State] >= rank[StateSuspicious]:
		return "", "the last security check found " + c.State + " signs", nil
	}
	st, err := s.stats(ctx)
	if err != nil {
		return "", "", err
	}
	if n := len(st.Snapshots); n > 0 && !strings.HasSuffix(st.Snapshots[n-1].Name, "-nightly") {
		return "", "the newest snapshot (" + st.Snapshots[n-1].Name + ") was taken since the last check; a new one would push out the checked one", nil
	}
	name, err := s.snapshotLocked(ctx, label)
	return name, "", err
}
