package changes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// Prompt context sizes.
const (
	contextLookback = 14 * 24 * time.Hour
	maxContext      = 8
)

var _ agent.ContextSource = (*Service)(nil)

// PromptContext tells her where her recent proposals stand, the owner's reason
// for any denial, and how many proposals today still allows. It is empty when
// she has proposed nothing lately.
func (s *Service) PromptContext(ctx context.Context, now time.Time) (string, error) {
	list, err := s.List(ctx, "")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	n := 0
	for _, c := range list {
		at, err := time.Parse(time.RFC3339, c.UpdatedAt)
		if err != nil || now.Sub(at) > contextLookback {
			continue
		}
		if n == maxContext {
			b.WriteString("- and older ones\n")
			break
		}
		n++
		fmt.Fprintf(&b, "- #%s %q (%s): %s", c.ID, oneLine(c.Title), c.Kind, c.Status)
		switch {
		case c.Status == StatusWorking && c.Stage != "":
			fmt.Fprintf(&b, ", %s", oneLine(c.Stage))
		case c.Result != nil:
			fmt.Fprintf(&b, ": %s", oneLine(*c.Result))
		}
		if c.PRURL != nil {
			fmt.Fprintf(&b, " (%s)", *c.PRURL)
		}
		if c.Feedback != nil {
			fmt.Fprintf(&b, "; the owner's reason: %q", oneLine(*c.Feedback))
		}
		b.WriteString("\n")
	}
	if n == 0 {
		return "", nil
	}
	left, err := s.left(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "Proposals left in the next 24 hours: %d of %d.", left, dailyCap)
	return b.String(), nil
}

// oneLine flattens text so it can't start lines of its own.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
