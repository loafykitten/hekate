package reflection

import (
	"context"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

const (
	contextAge  = 36 * time.Hour
	contextSize = 600
)

var _ agent.ContextSource = (*Service)(nil)

// PromptContext is her most recent diary entry, while it's fresh: it carries
// what she meant to remember or do into the next day.
func (s *Service) PromptContext(ctx context.Context, now time.Time) (string, error) {
	e, err := s.latestDiary(ctx, "")
	if err != nil || e == nil {
		return "", err
	}
	day, err := time.ParseInLocation(time.DateOnly, e.Day, s.loc)
	if err != nil || now.Sub(day) > contextAge+24*time.Hour {
		return "", nil //nolint:nilerr // an unreadable day is just not shown
	}
	return "Your diary entry for " + day.Format("Monday, January 2") + ": " + clip(oneLine(e.Text), contextSize), nil
}
