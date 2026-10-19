package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// Prompt context sizes.
const (
	followUpLookahead = 24 * time.Hour
	maxFollowUps      = 5
	maxPinned         = 5
	maxRecalled       = 6
)

var _ agent.QuerySource = (*Service)(nil)

// PromptContext is what she always knows. See PromptContextFor.
func (s *Service) PromptContext(ctx context.Context, now time.Time) (string, error) {
	return s.PromptContextFor(ctx, now, "")
}

// PromptContextFor builds the memory block of the prompt: the core profile,
// follow-ups that are due, the must-never-forget memories, and the memories
// that best match what the owner just said. It satisfies agent.QuerySource.
func (s *Service) PromptContextFor(ctx context.Context, now time.Time, userText string) (string, error) {
	profile, err := s.Profile(ctx)
	if err != nil {
		return "", err
	}
	followUps, err := s.FollowUpsDue(ctx, now.Add(followUpLookahead), maxFollowUps)
	if err != nil {
		return "", err
	}
	pinned, err := s.Pinned(ctx, maxPinned)
	if err != nil {
		return "", err
	}
	recalled, err := s.Search(ctx, userText, "", maxRecalled+len(followUps)+len(pinned))
	if err != nil {
		return "", err
	}

	// Each memory appears once, in the most specific section.
	shown := map[string]bool{}
	take := func(ms []Memory, limit int) []Memory {
		var out []Memory
		for _, m := range ms {
			if shown[m.ID] || len(out) >= limit {
				continue
			}
			shown[m.ID] = true
			out = append(out, m)
		}
		return out
	}
	followUps = take(followUps, maxFollowUps)
	pinned = take(pinned, maxPinned)
	recalled = take(recalled, maxRecalled)

	var b strings.Builder
	if len(profile) > 0 {
		b.WriteString("Profile:\n")
		for _, e := range profile {
			fmt.Fprintf(&b, "- %s: %s\n", e.Key, e.Value)
		}
	}
	section := func(title string, ms []Memory, withFollowUp bool) {
		if len(ms) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(title + "\n")
		for _, m := range ms {
			b.WriteString("- " + line(m, withFollowUp) + "\n")
		}
	}
	section("Worth asking about now (follow-ups that are due; after you have asked, clear follow_up_at or update the memory):", followUps, true)
	section("Never forget:", pinned, false)
	section("Remembered notes that may relate to what they just said:", recalled, false)
	return strings.TrimSpace(b.String()), nil
}

// line renders a memory for the model, id included so it can update or forget it.
func line(m Memory, withFollowUp bool) string {
	s := fmt.Sprintf("[%s] (%s) %s", m.ID, m.Kind, m.Content)
	if withFollowUp && m.FollowUpAt != nil {
		s += " (ask on " + *m.FollowUpAt + ")"
	}
	return s
}
