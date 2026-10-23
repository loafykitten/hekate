package lab

import (
	"context"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// contextFindings is how many findings her prompt shows; lab_status shows more.
const contextFindings = 4

var _ agent.ContextSource = (*Service)(nil)

// PromptContext tells her how her lab's last security check went: the verdict, and what to act
// on when there's something. It reads only the database, so it stays fast; lab_status asks the
// homelab for the rest.
func (s *Service) PromptContext(ctx context.Context, _ time.Time) (string, error) {
	if !s.Configured() {
		return "", nil
	}
	c, err := s.lastCheck(ctx)
	if err != nil {
		return "", err
	}
	if c != nil && c.State == StateClean && !c.Isolated {
		return "Last security check " + c.At.In(s.loc).Format("Mon Jan 2 15:04") + ": nothing to act on.", nil
	}
	var fs []Finding
	if c != nil {
		for _, f := range c.Findings {
			if rank[f.Level] > rank[levelInfo] {
				fs = append(fs, f)
			}
		}
		c = &Check{At: c.At, State: c.State, Findings: fs, Isolated: c.Isolated}
	}
	text := strings.TrimSpace(s.checkLines(c, contextFindings))
	if c != nil && rank[c.State] >= rank[StateSuspicious] {
		text += "\nBring it up with the owner; don't keep working in the lab until they've seen it."
	}
	return text, nil
}
