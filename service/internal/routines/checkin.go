package routines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/tasks"
	"github.com/loafykitten/hekate/service/internal/when"
)

// CheckInJob is the routine's name in job_runs and self_status.
const CheckInJob = "checkin"

// CheckInTool is the tool's name.
const CheckInTool = "evening_checkin"

const (
	// A check-in missed while the service was down is still asked within this.
	checkInCatchUp   = time.Hour
	checkInTimeout   = time.Minute
	maxCheckInTasks  = 8
	maxCheckInEvents = 6
	// Tomorrow's first commitments: this many events.
	maxTomorrow = 3
)

// checkInTitle is the notice, and the question she asks.
const checkInTitle = "How did today go?"

// CheckInJobDef is the routine: once a day at the owner's check-in time.
func (s *Service) CheckInJobDef() jobs.Job {
	return jobs.Job{Name: CheckInJob, Next: jobs.Daily(s.loc, checkInCatchUp, s.checkInAt), Run: s.AskCheckIn, Timeout: checkInTimeout}
}

// checkInAt is when the check-in is asked, or false when it's off.
func (s *Service) checkInAt(ctx context.Context) (jobs.Clock, bool) {
	st, err := s.Settings(ctx)
	if err != nil || !st.CheckIn {
		return jobs.Clock{}, false
	}
	c, err := jobs.ParseClock(st.CheckInAt)
	if err != nil {
		c, _ = jobs.ParseClock(defaults.CheckInAt)
	}
	return c, true
}

// talkedOverDay is the day a check-in at t looks back on: before 06:00 local
// time it's still the day before.
func (s *Service) talkedOverDay(t time.Time) string {
	local := t.In(s.loc)
	if local.Hour() < jobs.DayTurnHour {
		local = time.Date(local.Year(), local.Month(), local.Day()-1, 12, 0, 0, 0, s.loc)
	}
	return local.Format(time.DateOnly)
}

// AskCheckIn sends the evening check-in: one notice and push asking how the
// day went, which starts the check-in when tapped. It asks once a day, and not
// at all when the owner has already talked the day over with her.
func (s *Service) AskCheckIn(ctx context.Context) (string, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	if !st.CheckIn {
		return "The check-in is off", nil
	}
	day := s.talkedOverDay(s.now())
	for _, done := range []struct{ key, summary string }{
		{checkedKey, "Already talked the day over"},
		{askedKey, "Already asked about the day"},
	} {
		last, err := s.st.GetSetting(ctx, done.key)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if last == day {
			return done.summary, nil
		}
	}
	notice := proto.NewNotify("checkin-"+day, proto.NotifyKindCheckIn, checkInTitle, "", nil)
	// Only a tap in the app starts it: not for the backup channel.
	payload := push.Payload{Title: checkInTitle, Body: "Tap to talk it over.", URL: "/?checkin=" + day, Tag: "checkin", NoBackup: true}
	if !s.reach(ctx, notice, payload) {
		return "Couldn't reach the owner (no app open, no notification delivered)", nil
	}
	if err := s.st.SetSetting(ctx, askedKey, day); err != nil {
		return "", err
	}
	return "Asked how the day went", nil
}

// CheckIn opens the check-in the owner tapped: day is the day its notice
// asked about, "" for the current one. A notice from another day, or a day
// already talked over, starts nothing. The day counts as talked over once the
// check-in's call ran; a turn that fails or is cut short frees it again.
func (s *Service) CheckIn(ctx context.Context, day string) (agent.Opening, bool) {
	current := s.talkedOverDay(s.now())
	if day == "" {
		day = current
	}
	if day != current {
		return agent.Opening{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checking == day {
		return agent.Opening{}, false // one is under way
	}
	last, err := s.st.GetSetting(ctx, checkedKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("check-in: can't tell whether the day was talked over", "error", err)
		return agent.Opening{}, false
	}
	if last == day {
		return agent.Opening{}, false
	}
	s.checking = day
	return agent.Opening{Tool: CheckInTool, Settle: func(given bool) { s.settleCheckIn(day, given) }}, true
}

// settleCheckIn ends a tapped check-in; one that failed or was cut short
// leaves the day free for another.
func (s *Service) settleCheckIn(day string, given bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checking == day {
		s.checking = ""
	}
	if given {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if last, err := s.st.GetSetting(ctx, checkedKey); err == nil && last == day {
		if err := s.st.SetSetting(ctx, checkedKey, ""); err != nil {
			s.log.Warn("check-in: can't free the day for another try", "error", err)
		}
	}
}

// checkInTool gathers the day for the check-in; calling it counts as talking
// the day over, so the evening's notice isn't sent after all.
func (s *Service) checkInTool() agent.Tool {
	return agent.Tool{
		Name: CheckInTool,
		Description: "Gather the owner's day for the evening check-in: what they got done, what's still open, today's events, " +
			"things worth asking about and tomorrow's first commitments. Cards with the details are shown beside you. Call it " +
			"when the owner wants to talk their day over (\"let me tell you about today\", \"what did I get done today?\"), then " +
			"check in from what it returns.",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Effect: agent.EffectRead,
		Label:  "Looking back over your day",
		Run: func(ctx context.Context, _ json.RawMessage) (agent.Result, error) {
			now := s.now()
			if err := s.st.SetSetting(ctx, checkedKey, s.talkedOverDay(now)); err != nil {
				s.log.Warn("check-in: can't record that the day was talked over", "error", err)
			}
			facts, cards := s.gatherEvening(ctx, now)
			return agent.Result{Content: checkInGuide + facts, Cards: cards}, nil
		},
	}
}

const checkInGuide = `This is the evening check-in. Ask how today went, spoken, in your own voice: two to four short
sentences, warm and unhurried. You can name one or two things from the day (something they got done,
something that happened) so they know you noticed, and tomorrow's first commitment in passing if there
is one. Don't list or judge; the cards beside you show the details. If they've already started telling
you about their day, answer that instead of asking again. Then let them talk.

`

// gatherEvening lays out the day being talked over for the model and builds
// the cards that go with the check-in. A source that fails is left out and
// said so.
func (s *Service) gatherEvening(ctx context.Context, now time.Time) (string, []proto.Card) {
	local := now.In(s.loc)
	dayStart, _ := time.ParseInLocation(time.DateOnly, s.talkedOverDay(now), s.loc)
	dayEnd := dayStart.AddDate(0, 0, 1)
	tomorrowEnd := dayEnd.AddDate(0, 0, 1)
	var b strings.Builder
	var done, open, tomorrow *proto.Card // the cards, most important first
	unavailable := func(what string, err error) {
		s.log.Warn("check-in: a source failed", "source", what, "error", err)
		fmt.Fprintf(&b, "(%s couldn't be checked.)\n\n", what)
	}
	fmt.Fprintf(&b, "Looking back over %s. It's %s now.\n\n", dayStart.Format("Monday, January 2"), local.Format("Mon 15:04"))

	if s.tasks != nil {
		list, err := s.tasks.DoneSince(ctx, dayStart, maxCheckInTasks)
		if err != nil {
			unavailable("Finished tasks", err)
		} else {
			list = uniqueTasks(list)
			b.WriteString("## Done today\n")
			if len(list) == 0 {
				b.WriteString("Nothing marked done.\n")
			}
			for _, t := range list {
				b.WriteString("- " + oneLine(t.Title) + "\n")
			}
			b.WriteString("\n")
			if len(list) > 0 {
				done, _ = tasks.RecordCard("Done today", list)
			}
		}
	}

	if s.tasks != nil {
		list, err := s.tasks.List(ctx, tasks.Filter{Status: tasks.StatusOpen, DueBefore: &when.Value{T: tomorrowEnd}, Limit: 50})
		if err != nil {
			unavailable("Tasks", err)
		} else {
			var late, next []tasks.Task
			for _, t := range list {
				if t.Due == nil {
					continue
				}
				if due, err := when.Parse(*t.Due, s.loc); err == nil && !due.T.Before(dayEnd) {
					next = append(next, t)
				} else {
					late = append(late, t)
				}
			}
			late, next = late[:min(len(late), maxCheckInTasks)], next[:min(len(next), maxCheckInTasks)]
			b.WriteString("## Still open, due today or earlier\n")
			if len(late) == 0 {
				b.WriteString("None.\n")
			}
			for _, t := range late {
				b.WriteString("- " + oneLine(t.Title) + " (due " + *t.Due + ")\n")
			}
			b.WriteString("\n")
			if len(next) > 0 {
				b.WriteString("## Tasks due tomorrow\n")
				for _, t := range next {
					b.WriteString("- " + oneLine(t.Title) + " (due " + *t.Due + ")\n")
				}
				b.WriteString("\n")
			}
			if len(late) > 0 {
				open, _ = tasks.Card("Still open", late, nil)
			}
		}
	}

	if s.cal != nil && s.cal.Configured() {
		if events, err := s.cal.Events(ctx, dayStart, now); err != nil {
			unavailable("Today's calendar", err)
		} else if len(events) > 0 {
			b.WriteString("## Today's calendar\n")
			for _, e := range events[:min(len(events), maxCheckInEvents)] {
				b.WriteString("- " + s.describeEvent(e, dayStart) + "\n")
			}
			b.WriteString("\n")
		}
		ahead := now.Add(briefingAhead)
		if events, err := s.cal.Events(ctx, now, ahead); err != nil {
			unavailable("Tomorrow's calendar", err)
		} else {
			b.WriteString("## Coming up next, the next 24 hours\n")
			if len(events) == 0 {
				b.WriteString("Nothing scheduled.\n")
			}
			events = events[:min(len(events), maxTomorrow)]
			for _, e := range events {
				b.WriteString("- " + s.describeEvent(e, local) + "\n")
			}
			b.WriteString("\n")
			if len(events) > 0 {
				tomorrow = s.cardOf(s.cal, "events:checkin", "Coming up", now, ahead, events)
			}
		}
	}

	if s.followUps != nil {
		due, err := s.followUps.FollowUpsDue(ctx, now, 5)
		if err == nil && len(due) > 0 {
			b.WriteString("## Worth asking about\n")
			for _, m := range due {
				b.WriteString("- " + oneLine(m.Content) + "\n")
			}
			b.WriteString("\n")
		}
	}

	// The app shows the newest card on top: the most important goes last.
	var cards []proto.Card
	for _, c := range []*proto.Card{tomorrow, open, done} {
		if c != nil {
			cards = append(cards, *c)
		}
	}
	return b.String(), cards
}

// uniqueTasks keeps each task once: a repeating one done twice today shows once.
func uniqueTasks(list []tasks.Task) []tasks.Task {
	seen := map[string]bool{}
	out := list[:0:0]
	for _, t := range list {
		if !seen[t.ID] {
			seen[t.ID] = true
			out = append(out, t)
		}
	}
	return out
}
