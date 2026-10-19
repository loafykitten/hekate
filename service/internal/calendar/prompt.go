package calendar

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const promptCacheTTL = 60 * time.Second

// PromptContext returns today's and tomorrow's events as compact text for the
// system prompt. It is empty when the calendar is not configured. The result
// is cached for a minute and dropped whenever Hekate changes the calendar.
func (s *Service) PromptContext(ctx context.Context, now time.Time) (string, error) {
	if s.dav == nil {
		return "", nil
	}
	now = now.In(s.loc)
	day := dayStart(now, s.loc)
	key := day.Format(dateLayout)

	s.promptMu.Lock()
	if s.promptKey == key && s.now().Sub(s.promptStamp) < promptCacheTTL {
		text := s.promptText
		s.promptMu.Unlock()
		return text, nil
	}
	s.promptMu.Unlock()

	occ, err := s.occurrences(ctx, day, day.AddDate(0, 0, 2))
	if err != nil {
		return "", err
	}
	text := s.render(occ, day)

	s.promptMu.Lock()
	s.promptKey, s.promptText, s.promptStamp = key, text, s.now()
	s.promptMu.Unlock()
	return text, nil
}

func (s *Service) render(occ []occurrence, day time.Time) string {
	tomorrow := day.AddDate(0, 0, 1)
	var today, next []string
	for _, o := range occ {
		line := promptLine(o, s.loc)
		// An event belongs to the day it starts; a multi-day or running event
		// also shows on the later day.
		if overlaps(o.start, o.end, day, tomorrow) {
			today = append(today, line)
		}
		if overlaps(o.start, o.end, tomorrow, tomorrow.AddDate(0, 0, 1)) {
			next = append(next, line)
		}
	}
	return fmt.Sprintf("Calendar today (%s): %s\nCalendar tomorrow (%s): %s",
		day.Format("Mon Jan 2"), joinOrNone(today),
		tomorrow.Format("Mon Jan 2"), joinOrNone(next))
}

func joinOrNone(l []string) string {
	if len(l) == 0 {
		return "nothing scheduled"
	}
	return strings.Join(l, "; ")
}

// promptLine renders one event compactly, e.g. "15:00-16:00 Dentist @ Main St".
func promptLine(o occurrence, loc *time.Location) string {
	var when string
	if o.AllDay {
		when = "all day"
	} else {
		when = o.start.In(loc).Format("15:04")
		if o.end.After(o.start) {
			when += "-" + o.end.In(loc).Format("15:04")
		}
	}
	line := when + " " + oneLine(o.Title, 80)
	if o.Location != "" {
		line += " @ " + oneLine(o.Location, 60)
	}
	return line
}

// oneLine flattens text from the calendar (which others can write into by
// inviting the owner) to a single short line.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}
