package routines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/mail"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/tasks"
	"github.com/loafykitten/hekate/service/internal/when"
)

// HeartbeatJob is the routine's name in job_runs and self_status.
const HeartbeatJob = "heartbeat"

const (
	heartbeatEvery   = 30 * time.Minute
	heartbeatTimeout = 2 * time.Minute
	// Events with a place to get to, starting in this window, are worth a heads-up.
	eventSoonest = 10 * time.Minute
	eventLatest  = 75 * time.Minute
	// A timed task is overdue once it's this late.
	overdueGrace = time.Hour
	// Mail that needs the owner gets a nudge once it has waited this long unread,
	// and not once it's older than a day.
	mailWait   = 20 * time.Minute
	mailMaxAge = 24 * time.Hour
	// What was nudged is remembered a year, so nothing still due comes round again.
	keepNudges   = 365 * 24 * time.Hour
	maxFollowUps = 100
	maxLines     = 4
	maxLine      = 140
)

// What the heartbeat reads; the feature services satisfy these.
type (
	TaskSource interface {
		List(ctx context.Context, f tasks.Filter) ([]tasks.Task, error)
	}
	EventSource interface {
		Configured() bool
		Events(ctx context.Context, from, to time.Time) ([]calendar.Event, error)
	}
	MailSource interface {
		Configured() bool
		Summary(ctx context.Context, limit int) (mail.Summary, error)
	}
	FollowUpSource interface {
		FollowUpsDue(ctx context.Context, until time.Time, limit int) ([]memory.Memory, error)
	}
)

// nudge is one thing worth telling the owner about, once.
type nudge struct {
	key   string // never nudged twice
	kind  string // event, task, mail, follow_up
	title string // the notice's title when it's the only one
	line  string
}

// HeartbeatJobDef is the routine: every half hour, quiet outside waking hours.
func (s *Service) HeartbeatJobDef() jobs.Job {
	return jobs.Job{Name: HeartbeatJob, Next: jobs.Every(heartbeatEvery), Run: s.Heartbeat, Timeout: heartbeatTimeout}
}

// Heartbeat looks for what needs the owner (an event to leave for, an overdue
// task, mail that needs them, something worth following up) and sends one
// notice about whatever it hasn't mentioned before. Nothing new is a quiet,
// successful run. Outside waking hours, or switched off, it doesn't run.
func (s *Service) Heartbeat(ctx context.Context) (string, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	now := s.now()
	if !st.Heartbeat || !st.awake(now, s.loc) {
		return "", jobs.ErrSkip
	}
	var found []nudge
	var failed []string
	for _, check := range []struct {
		name string
		run  func(context.Context, time.Time) ([]nudge, error)
	}{
		{"calendar", s.eventNudges}, {"tasks", s.taskNudges}, {"mail", s.mailNudges}, {"follow-ups", s.followUpNudges},
	} {
		n, err := check.run(ctx, now)
		if err != nil {
			s.log.Warn("heartbeat check failed", "check", check.name, "error", err)
			failed = append(failed, check.name)
			continue
		}
		found = append(found, n...)
	}
	if len(failed) == 4 {
		return "", fmt.Errorf("every check failed: %s", strings.Join(failed, ", "))
	}
	fresh, err := s.unsent(ctx, found)
	if err != nil {
		return "", err
	}
	summary := "Nothing worth a nudge"
	if len(fresh) > 0 {
		shownCount, err := s.deliver(ctx, now, fresh)
		if err != nil {
			return "", err
		}
		summary = fmt.Sprintf("Nudged about %d thing(s)", shownCount)
	}
	if len(failed) > 0 {
		summary += fmt.Sprintf(" (couldn't check %s)", strings.Join(failed, ", "))
	}
	return summary, nil
}

func (s *Service) eventNudges(ctx context.Context, now time.Time) ([]nudge, error) {
	if s.cal == nil || !s.cal.Configured() {
		return nil, nil
	}
	events, err := s.cal.Events(ctx, now.Add(eventSoonest), now.Add(eventLatest))
	if err != nil {
		return nil, err
	}
	var out []nudge
	for _, e := range events {
		start, err := time.Parse(time.RFC3339, e.Start)
		if e.AllDay || strings.TrimSpace(e.Location) == "" || err != nil || start.Before(now.Add(eventSoonest)) {
			continue
		}
		mins := int(start.Sub(now).Round(5 * time.Minute).Minutes())
		out = append(out, nudge{
			key: "event:" + e.UID + ":" + e.Start, kind: "event", title: "Time to get ready",
			line: clip(fmt.Sprintf("%s at %s, %s (in %d min)", e.Title, start.In(s.loc).Format("3:04 PM"), oneLine(e.Location), mins), maxLine),
		})
	}
	return out, nil
}

func (s *Service) taskNudges(ctx context.Context, now time.Time) ([]nudge, error) {
	if s.tasks == nil {
		return nil, nil
	}
	list, err := s.tasks.List(ctx, tasks.Filter{Status: tasks.StatusOpen, DueBefore: &when.Value{T: now.Add(-overdueGrace)}})
	if err != nil {
		return nil, err
	}
	today := now.In(s.loc).Format(time.DateOnly)
	var out []nudge
	for _, t := range list {
		if t.Due == nil || (len(*t.Due) == len(time.DateOnly) && *t.Due >= today) {
			continue // due today (all day) isn't late yet
		}
		out = append(out, nudge{key: "task:" + t.ID + ":" + *t.Due, kind: "task", title: "Overdue", line: t.Title})
	}
	return out, nil
}

func (s *Service) mailNudges(ctx context.Context, now time.Time) ([]nudge, error) {
	if s.mail == nil || !s.mail.Configured() {
		return nil, nil
	}
	sum, err := s.mail.Summary(ctx, 10)
	if err != nil {
		return nil, err
	}
	var out []nudge
	for _, m := range sum.Attention {
		at, err := time.Parse(time.RFC3339, m.Date)
		if err != nil || now.Sub(at) < mailWait || now.Sub(at) > mailMaxAge {
			continue
		}
		who := m.From.Name
		if who == "" {
			who = m.From.Address
		}
		out = append(out, nudge{key: "mail:" + m.ID, kind: "mail", title: "Mail that needs you", line: clip(oneLine(who+": "+m.Subject), maxLine)})
	}
	return out, nil
}

func (s *Service) followUpNudges(ctx context.Context, now time.Time) ([]nudge, error) {
	if s.followUps == nil {
		return nil, nil
	}
	// Plenty: ones already nudged are dropped afterwards, and mustn't crowd out the rest.
	due, err := s.followUps.FollowUpsDue(ctx, now, maxFollowUps)
	if err != nil {
		return nil, err
	}
	out := make([]nudge, 0, len(due))
	for _, m := range due {
		at := ""
		if m.FollowUpAt != nil {
			at = *m.FollowUpAt
		}
		out = append(out, nudge{key: "follow_up:" + m.ID + ":" + at, kind: "follow_up", title: "When you have a moment", line: clip(oneLine(m.Content), maxLine)})
	}
	return out, nil
}

// unsent drops nudges already sent.
func (s *Service) unsent(ctx context.Context, all []nudge) ([]nudge, error) {
	var out []nudge
	for _, n := range all {
		var one int
		err := s.st.DB().QueryRowContext(ctx, `SELECT 1 FROM nudges WHERE key = ?`, n.key).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("check nudges: %w", err)
		}
		out = append(out, n)
	}
	return out, nil
}

// deliver sends one notice for the nudges and records the ones it names, so
// none repeats; it returns how many it named.
func (s *Service) deliver(ctx context.Context, now time.Time, list []nudge) (int, error) {
	title, body, shown := compose(list)
	id := "nudge-" + now.UTC().Format("20060102T150405")
	s.events.Notify(proto.NewNotify(id, proto.NotifyKindNudge, title, body, nil))
	if s.pusher != nil {
		res, err := s.pusher.Send(ctx, push.Payload{Title: title, Body: body, URL: "/?menu=today", Tag: "nudge"}, push.UrgencyNormal)
		if err != nil {
			s.log.Warn("heartbeat push failed", "error", err, "sent", res.Sent)
		}
	}
	db := s.st.DB()
	for _, n := range shown {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO nudges (key, sent_at) VALUES (?, ?)`, n.key, stamp(now)); err != nil {
			return 0, fmt.Errorf("record a nudge: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM nudges WHERE sent_at < ?`, stamp(now.Add(-keepNudges))); err != nil {
		s.log.Warn("cannot prune old nudges", "error", err)
	}
	return len(shown), nil
}

// compose turns nudges into a notice: overdue tasks and waiting mail are
// grouped into a line each (naming up to three), everything else gets its own
// line, at most maxLines. It returns the nudges the notice names; the rest stay
// pending for a later heartbeat.
func compose(list []nudge) (title, body string, shown []nudge) {
	type line struct {
		text  string
		named []nudge
	}
	var lines []line
	group := func(kind, one, many string) {
		var items []nudge
		for _, n := range list {
			if n.kind == kind {
				items = append(items, n)
			}
		}
		switch len(items) {
		case 0:
		case 1:
			lines = append(lines, line{one + items[0].line, items})
		default:
			named := items[:min(len(items), 3)]
			texts := make([]string, len(named))
			for i, n := range named {
				texts[i] = n.line
			}
			text := fmt.Sprintf(many, len(items)) + strings.Join(texts, "; ")
			if len(items) > len(named) {
				text += fmt.Sprintf(" and %d more", len(items)-len(named))
			}
			lines = append(lines, line{clip(text, maxLine*2), named})
		}
	}
	for _, n := range list {
		if n.kind == "event" {
			lines = append(lines, line{n.line, []nudge{n}})
		}
	}
	group("task", "Overdue: ", "%d overdue: ")
	group("mail", "Needs a reply or a look: ", "%d emails need you: ")
	for _, n := range list {
		if n.kind == "follow_up" {
			lines = append(lines, line{"On my mind: " + n.line, []nudge{n}})
		}
	}
	more := 0
	if len(lines) > maxLines {
		for _, l := range lines[maxLines-1:] {
			more += len(l.named)
		}
		lines = lines[:maxLines-1]
	}
	texts := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		texts = append(texts, l.text)
		shown = append(shown, l.named...)
	}
	if more > 0 {
		texts = append(texts, fmt.Sprintf("and %d more", more))
	}
	title = "A few things"
	if len(list) == 1 {
		title = list[0].title
	}
	return title, strings.Join(texts, "\n"), shown
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
