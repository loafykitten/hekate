package approvals

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// Prompt context sizes.
const (
	decidedLookback = 12 * time.Hour
	maxPending      = 8
	maxDecided      = 6
)

var _ agent.ContextSource = (*Service)(nil)

// PromptContext tells her what waits on the owner and what was decided in the
// last 12 hours and how it went. It is empty when there is nothing to say.
func (s *Service) PromptContext(ctx context.Context, now time.Time) (string, error) {
	pending, err := s.List(ctx, ListPending)
	if err != nil {
		return "", err
	}
	recent, err := s.List(ctx, ListRecent)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, a := range pending {
		if i == maxPending {
			fmt.Fprintf(&b, "- and %d more\n", len(pending)-maxPending)
			break
		}
		fmt.Fprintf(&b, "- %s (asked %s)\n", a.Title, ago(now, a.CreatedAt))
	}
	n := 0
	for _, a := range recent {
		at, err := time.Parse(time.RFC3339, *a.DecidedAt)
		if err != nil || now.Sub(at) > decidedLookback || n == maxDecided {
			continue
		}
		if n == 0 && b.Len() > 0 {
			b.WriteString("\n")
		}
		if n == 0 {
			b.WriteString("Decided recently:\n")
		}
		n++
		fmt.Fprintf(&b, "- %s (%s)\n", outcome(a), ago(now, *a.DecidedAt))
	}
	return strings.TrimSpace(b.String()), nil
}

// outcome says how a decided approval went, e.g. "allowed: Move 14 messages → Moved 14 messages".
func outcome(a Approval) string {
	verb := "allowed"
	switch {
	case a.Status == StatusDenied:
		return "denied: " + a.Title
	case a.Status == StatusExpired:
		return "expired unanswered: " + a.Title
	case a.Status == StatusRunning:
		return "allowed, still running: " + a.Title
	case a.DecidedBy != nil && *a.DecidedBy == DecidedByRule:
		verb = "ran under a standing rule"
	}
	if a.Status == StatusFailed {
		verb += ", but it failed"
	}
	line := verb + ": " + a.Title
	if a.Result != nil && *a.Result != "" {
		line += " → " + *a.Result
	}
	return line
}

func ago(now time.Time, stamp string) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return "earlier"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
