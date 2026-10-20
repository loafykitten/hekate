package reminders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/when"
)

// missedWindow is how late a reminder may be after the service was down and
// still fire. Later than that it would be noise ("call the dentist" at 3 pm
// for a 9 am reminder is still useful; yesterday's is not).
const missedWindow = 12 * time.Hour

// Run is the scheduler. It fires what was missed while the service was down,
// then sleeps until the next reminder is due, waking early when the schedule
// changes. It returns when ctx is done.
func (s *Service) Run(ctx context.Context) {
	if _, err := s.fireDue(ctx, missedWindow); err != nil && ctx.Err() == nil {
		s.log.Error("cannot fire missed reminders", "error", err)
	}
	for ctx.Err() == nil {
		wait := maxSleep
		next, ok, err := s.nextAt(ctx)
		switch {
		case err != nil:
			s.log.Error("cannot read the next reminder", "error", err)
			wait = retryAfter
		case ok:
			if d := next.Sub(s.now()); d <= 0 {
				if _, err := s.fireDue(ctx, 0); err != nil && ctx.Err() == nil {
					s.log.Error("cannot fire reminders", "error", err)
					wait = retryAfter
				} else {
					continue
				}
			} else {
				wait = min(d, maxSleep)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-s.after(wait):
		}
	}
}

// nextAt is when the earliest scheduled reminder is due.
func (s *Service) nextAt(ctx context.Context) (time.Time, bool, error) {
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT at FROM reminders WHERE status = 'scheduled' ORDER BY at LIMIT 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("next reminder: %w", err)
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("next reminder time %q: %w", at, err)
	}
	return t, true, nil
}

type due struct {
	id          int64
	text, rrule string
	at, start   time.Time
	atStamp     string
}

// loadDue reads reminder id if it is still scheduled and due at now.
func (s *Service) loadDue(ctx context.Context, id int64, now time.Time) (due, bool, error) {
	d := due{id: id}
	var start string
	err := s.db.QueryRowContext(ctx, `SELECT text, at, start, COALESCE(rrule, '') FROM reminders
		WHERE id = ? AND status = 'scheduled' AND at <= ?`, id, stamp(now)).Scan(&d.text, &d.atStamp, &start, &d.rrule)
	if errors.Is(err, sql.ErrNoRows) {
		return due{}, false, nil
	}
	if err != nil {
		return due{}, false, fmt.Errorf("reload reminder %d: %w", id, err)
	}
	d.at, _ = time.Parse(time.RFC3339, d.atStamp) // written by this package
	d.start, _ = time.Parse(time.RFC3339, start)
	return d, true, nil
}

// fireDue fires every reminder that is due now. With maxLate > 0 (startup),
// one that is later than that is closed without notifying.
func (s *Service) fireDue(ctx context.Context, maxLate time.Duration) (int, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, at, start, COALESCE(rrule, '') FROM reminders
		WHERE status = 'scheduled' AND at <= ? ORDER BY at, id`, stamp(now))
	if err != nil {
		return 0, fmt.Errorf("find due reminders: %w", err)
	}
	var dues []due
	for rows.Next() {
		var d due
		var start string
		if err := rows.Scan(&d.id, &d.text, &d.atStamp, &start, &d.rrule); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("find due reminders: %w", err)
		}
		d.at, _ = time.Parse(time.RFC3339, d.atStamp) // written by this package
		d.start, _ = time.Parse(time.RFC3339, start)
		dues = append(dues, d)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, fmt.Errorf("find due reminders: %w", err)
	}

	fired := 0
	for _, d := range dues {
		ok, err := s.fire(ctx, d, now, maxLate)
		if err != nil {
			return fired, err
		}
		if ok {
			fired++
		}
	}
	return fired, nil
}

// fire closes one due reminder and, unless it is too stale, announces it. The
// database changes first and the guarded update makes a concurrent edit win,
// so a reminder is never announced twice or after being cancelled.
func (s *Service) fire(ctx context.Context, snap due, now time.Time, maxLate time.Duration) (bool, error) {
	s.mu.Lock()
	// Re-read under the lock every mutation takes: an edit between the query
	// and now (a removed repeat rule, new text) must win, not be overwritten
	// with the snapshot's values.
	d, ok, err := s.loadDue(ctx, snap.id, now)
	if err != nil || !ok {
		s.mu.Unlock()
		return false, err
	}
	notify := maxLate <= 0 || now.Sub(d.at) <= maxLate

	status, nextAt, start, rule := StatusFired, d.atStamp, stamp(d.start), ""
	switch {
	case d.rrule == "":
	case d.start.After(now):
		// A snoozed fire: the next occurrence on the rule's grid has not come
		// yet, so the series resumes there rather than skipping it.
		status, nextAt = StatusScheduled, stamp(d.start)
	default:
		r, err := when.ParseRule(d.rrule, d.start.In(s.loc), s.loc)
		if err != nil {
			s.log.Error("reminder has an unreadable repeat rule; firing it once", "id", d.id, "error", err)
		} else if next, ok := r.Next(now); ok {
			status, nextAt, start, rule = StatusScheduled, stamp(next), stamp(next), r.Rebase(next)
		}
	}
	lastFired := ""
	if notify {
		lastFired = stamp(now) // a missed one never went out, so it has no fire time
	}
	res, err := s.db.ExecContext(ctx, `UPDATE reminders SET status = ?, at = ?, start = ?, rrule = COALESCE(NULLIF(?, ''), rrule),
		last_fired_at = COALESCE(NULLIF(?, ''), last_fired_at), updated_at = ? WHERE id = ? AND status = 'scheduled' AND at = ?`,
		status, nextAt, start, rule, lastFired, stamp(now), d.id, d.atStamp)
	s.mu.Unlock()
	if err != nil {
		return false, fmt.Errorf("fire reminder %d: %w", d.id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil // changed under us
	}
	s.events.Invalidate(proto.DomainReminders)

	if !notify {
		s.log.Warn("reminder was missed while the service was down; closed without notifying",
			"id", d.id, "was_due", d.at.In(s.loc).Format(time.RFC3339), "late", now.Sub(d.at).Round(time.Minute).String())
		return false, nil
	}
	if now.Sub(d.at) > time.Minute {
		s.log.Info("firing a late reminder", "id", d.id, "late", now.Sub(d.at).Round(time.Second).String())
	}
	s.deliver(ctx, d)
	return true, nil
}

// deliver tells connected clients and every subscribed device.
func (s *Service) deliver(ctx context.Context, d due) {
	r, err := s.Get(ctx, d.id)
	if err != nil {
		s.log.Error("cannot load the reminder that fired", "id", d.id, "error", err)
		return
	}
	card, err := Card(notifyTitle, []Reminder{r}, []string{r.ID})
	if err != nil {
		s.log.Error("cannot build the reminder card", "id", d.id, "error", err)
	}
	id := strconv.FormatInt(d.id, 10)
	s.events.Notify(proto.NewNotify("reminder-"+id+"-"+strconv.FormatInt(s.now().Unix(), 10),
		proto.NotifyKindReminder, notifyTitle, d.text, card))

	if s.pusher == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, deliverTimout)
	defer cancel()
	res, err := s.pusher.Send(pctx, push.Payload{Title: notifyTitle, Body: d.text, URL: "/", Tag: "reminder-" + id}, push.UrgencyHigh)
	if err != nil {
		s.log.Error("cannot send push for reminder", "id", d.id, "error", err)
		return
	}
	s.log.Info("reminder fired", "id", d.id, "push_sent", res.Sent, "push_removed", res.Removed, "push_failed", res.Failed)
}
