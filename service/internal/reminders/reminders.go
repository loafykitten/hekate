// Package reminders stores reminders, fires them on time and delivers them by
// push and in-app notification. A repeating reminder is rescheduled each time
// it fires.
package reminders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/when"
)

// Reminder statuses.
const (
	StatusScheduled = "scheduled"
	StatusFired     = "fired"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// Limits.
const (
	maxText       = 500
	maxSnooze     = 7 * 24 * 60 // minutes
	maxRows       = 200
	maxSleep      = time.Minute // re-check regularly in case the wall clock jumps
	retryAfter    = 5 * time.Second
	notifyTitle   = "Reminder"
	deliverTimout = 30 * time.Second
)

// Reminder is the API shape from PROTOCOL.md.
type Reminder struct {
	ID          string  `json:"id"`
	Text        string  `json:"text"`
	At          string  `json:"at"`
	RRule       *string `json:"rrule"`
	Status      string  `json:"status"`
	LastFiredAt *string `json:"last_fired_at"`
	CreatedAt   string  `json:"created_at"`
}

// Pusher delivers web push; push.Service satisfies it.
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Options are the dependencies of a Service.
type Options struct {
	Store    *store.Store
	Location *time.Location
	Events   proto.Events
	Push     Pusher // nil: in-app notifications only
	Logger   *slog.Logger
	// Now and After are the clock. Tests replace them; After(d) must behave
	// like time.After.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// Service owns reminders and the scheduler.
type Service struct {
	db     *sql.DB
	loc    *time.Location
	events proto.Events
	pusher Pusher
	log    *slog.Logger
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time

	mu   sync.Mutex    // serializes read-modify-write sequences
	wake chan struct{} // tells the scheduler the schedule changed
}

// New builds the service. Call Run to start the scheduler.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("reminders: Store is required")
	}
	s := &Service{db: o.Store.DB(), loc: o.Location, events: o.Events, pusher: o.Push, log: o.Logger,
		now: o.Now, after: o.After, wake: make(chan struct{}, 1)}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.events == nil {
		s.events = proto.NopEvents{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.after == nil {
		s.after = time.After
	}
	return s, nil
}

const columns = `id, text, at, rrule, status, last_fired_at, created_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row scanner) (Reminder, error) {
	var (
		r                  Reminder
		id                 int64
		at, created        string
		rrule, lastFiredAt sql.NullString
	)
	if err := row.Scan(&id, &r.Text, &at, &rrule, &r.Status, &lastFiredAt, &created); err != nil {
		return Reminder{}, err //nolint:wrapcheck // callers distinguish sql.ErrNoRows
	}
	r.ID = strconv.FormatInt(id, 10)
	r.At, r.CreatedAt = s.display(at), s.display(created)
	if rrule.Valid && rrule.String != "" {
		r.RRule = &rrule.String
	}
	if lastFiredAt.Valid {
		l := s.display(lastFiredAt.String)
		r.LastFiredAt = &l
	}
	return r, nil
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Service) display(stored string) string {
	t, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		return stored
	}
	return t.In(s.loc).Format(time.RFC3339)
}

func notFound(id int64) error { return httpx.NotFound("there is no reminder with id %d", id) }

// Get returns one reminder.
func (s *Service) Get(ctx context.Context, id int64) (Reminder, error) {
	r, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM reminders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Reminder{}, notFound(id)
	}
	if err != nil {
		return Reminder{}, fmt.Errorf("get reminder %d: %w", id, err)
	}
	return r, nil
}

// Queries are constants: only the limit is bound.
const (
	upcomingQuery = `SELECT ` + columns + ` FROM reminders WHERE status = 'scheduled' ORDER BY at, id LIMIT ?`
	pastQuery     = `SELECT ` + columns + ` FROM reminders WHERE status <> 'scheduled' ORDER BY COALESCE(last_fired_at, at) DESC, id DESC LIMIT ?`
)

// List filters: upcoming are scheduled ones soonest first; past are everything
// else, newest first; all is upcoming followed by past.
const (
	ListUpcoming = "upcoming"
	ListPast     = "past"
	ListAll      = "all"
)

// List returns reminders by which = ListUpcoming (the default), ListPast or ListAll.
func (s *Service) List(ctx context.Context, which string) ([]Reminder, error) {
	switch which {
	case "", ListUpcoming:
		return s.query(ctx, upcomingQuery)
	case ListPast:
		return s.query(ctx, pastQuery)
	case ListAll:
		up, err := s.query(ctx, upcomingQuery)
		if err != nil {
			return nil, err
		}
		past, err := s.query(ctx, pastQuery)
		return append(up, past...), err
	}
	return nil, httpx.BadRequest("status must be upcoming, past or all")
}

func (s *Service) query(ctx context.Context, q string) ([]Reminder, error) {
	rows, err := s.db.QueryContext(ctx, q, maxRows)
	if err != nil {
		return nil, fmt.Errorf("list reminders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Reminder{}
	for rows.Next() {
		r, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list reminders: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reminders: %w", err)
	}
	return out, nil
}

// futureTime parses at as a time of day that has not passed yet.
func (s *Service) futureTime(at string, now time.Time) (time.Time, error) {
	v, err := when.Parse(at, s.loc)
	if err != nil {
		return time.Time{}, httpx.BadRequest("at: %s", err)
	}
	if v.DateOnly {
		return time.Time{}, httpx.BadRequest("at needs a time of day, e.g. %s", v.T.Format("2006-01-02")+"T09:00")
	}
	if !v.T.After(now) {
		return time.Time{}, httpx.BadRequest("%s has already passed (it is %s now)", v.T.Format(time.RFC3339), now.In(s.loc).Format(time.RFC3339))
	}
	return v.T, nil
}

func checkText(text string) (string, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", httpx.BadRequest("text is required")
	case utf8.RuneCountInString(text) > maxText:
		return "", httpx.BadRequest("text is longer than %d characters", maxText)
	}
	return text, nil
}

// checkRule validates a repeat rule anchored at start and returns its canonical form.
func (s *Service) checkRule(rule string, start time.Time) (string, error) {
	r, err := when.ParseRule(rule, start, s.loc)
	if err != nil {
		return "", httpx.BadRequest("rrule: %s", err)
	}
	return r.String(), nil
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) changed() {
	s.poke()
	s.events.Invalidate(proto.DomainReminders)
}

// Create schedules a reminder at a future time. rule may be empty.
func (s *Service) Create(ctx context.Context, text, at, rule string) (Reminder, error) {
	return s.create(ctx, text, func(now time.Time) (time.Time, error) { return s.futureTime(at, now) }, rule)
}

// CreateIn schedules a reminder the given number of minutes from now.
func (s *Service) CreateIn(ctx context.Context, text string, minutes int, rule string) (Reminder, error) {
	return s.create(ctx, text, func(now time.Time) (time.Time, error) { return inMinutes(now, minutes) }, rule)
}

func inMinutes(now time.Time, minutes int) (time.Time, error) {
	if minutes < 1 || minutes > 366*24*60 {
		return time.Time{}, httpx.BadRequest("in_minutes must be between 1 and %d", 366*24*60)
	}
	return now.Add(time.Duration(minutes) * time.Minute).Truncate(time.Second), nil
}

func (s *Service) create(ctx context.Context, text string, when func(time.Time) (time.Time, error), rule string) (Reminder, error) {
	text, err := checkText(text)
	if err != nil {
		return Reminder{}, err
	}
	now := s.now()
	at, err := when(now)
	if err != nil {
		return Reminder{}, err
	}
	var ruleArg any
	if strings.TrimSpace(rule) != "" {
		canon, err := s.checkRule(rule, at)
		if err != nil {
			return Reminder{}, err
		}
		ruleArg = canon
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO reminders (text, at, start, rrule, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'scheduled', ?, ?)`, text, stamp(at), stamp(at), ruleArg, stamp(now), stamp(now))
	if err != nil {
		return Reminder{}, fmt.Errorf("insert reminder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Reminder{}, fmt.Errorf("insert reminder: %w", err)
	}
	s.changed()
	return s.Get(ctx, id)
}

// Patch changes a reminder. Nil fields stay; for RRule the empty string
// stops repeating. At and InMinutes both reschedule (and re-arm a fired or
// cancelled reminder); give at most one.
type Patch struct {
	Text      *string
	At        *string
	InMinutes *int
	RRule     *string
}

// Update changes a reminder.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Reminder{}, err
	}
	if p.At != nil && p.InMinutes != nil {
		return Reminder{}, httpx.BadRequest("give either at or in_minutes, not both")
	}
	now := s.now()
	text := cur.Text
	if p.Text != nil {
		if text, err = checkText(*p.Text); err != nil {
			return Reminder{}, err
		}
	}
	curAt, err := time.Parse(time.RFC3339, cur.At)
	if err != nil {
		return Reminder{}, fmt.Errorf("reminder %d has an unreadable time: %w", id, err)
	}
	at, status := curAt, cur.Status
	resched := false
	switch {
	case p.At != nil:
		if at, err = s.futureTime(*p.At, now); err != nil {
			return Reminder{}, err
		}
		resched = true
	case p.InMinutes != nil:
		if at, err = inMinutes(now, *p.InMinutes); err != nil {
			return Reminder{}, err
		}
		resched = true
	}
	if resched {
		status = StatusScheduled
	}

	ruleText := ""
	if cur.RRule != nil {
		ruleText = *cur.RRule
	}
	if p.RRule != nil {
		ruleText = strings.TrimSpace(*p.RRule)
	}
	if ruleText != "" {
		if ruleText, err = s.checkRule(ruleText, at); err != nil {
			return Reminder{}, err
		}
	}
	var rule any
	if ruleText != "" {
		rule = ruleText
	}

	// The repeat rule is anchored where the owner put the reminder: a new time
	// or rule moves the anchor, a snooze does not.
	if resched || p.RRule != nil {
		_, err = s.db.ExecContext(ctx, `UPDATE reminders SET text = ?, at = ?, start = ?, rrule = ?, status = ?, updated_at = ? WHERE id = ?`,
			text, stamp(at), stamp(at), rule, status, stamp(now), id)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE reminders SET text = ?, rrule = ?, updated_at = ? WHERE id = ?`,
			text, rule, stamp(now), id)
	}
	if err != nil {
		return Reminder{}, fmt.Errorf("update reminder %d: %w", id, err)
	}
	s.changed()
	return s.Get(ctx, id)
}

// Snooze moves a reminder to minutes from now and schedules it again.
func (s *Service) Snooze(ctx context.Context, id int64, minutes int) (Reminder, error) {
	if minutes < 1 || minutes > maxSnooze {
		return Reminder{}, httpx.BadRequest("minutes must be between 1 and %d", maxSnooze)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Get(ctx, id); err != nil {
		return Reminder{}, err
	}
	now := s.now()
	at, _ := inMinutes(now, minutes) // range checked above
	// start is left alone: the repeat rule keeps its original time of day.
	if _, err := s.db.ExecContext(ctx, `UPDATE reminders SET at = ?, status = 'scheduled', updated_at = ? WHERE id = ?`,
		stamp(at), stamp(now), id); err != nil {
		return Reminder{}, fmt.Errorf("snooze reminder %d: %w", id, err)
	}
	s.changed()
	return s.Get(ctx, id)
}

// Done acknowledges a reminder. A one-off is finished (even if it had not
// fired yet). A repeating one keeps its schedule: it has already been moved on
// to the next occurrence, so acknowledging this one must not skip that one.
func (s *Service) Done(ctx context.Context, id int64) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Reminder{}, err
	}
	if cur.RRule != nil && cur.Status == StatusScheduled {
		return cur, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE reminders SET status = 'done', updated_at = ? WHERE id = ?`,
		stamp(s.now()), id); err != nil {
		return Reminder{}, fmt.Errorf("finish reminder %d: %w", id, err)
	}
	s.changed()
	return s.Get(ctx, id)
}

// Cancel stops a reminder, and a repeating one for good.
func (s *Service) Cancel(ctx context.Context, id int64) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Get(ctx, id); err != nil {
		return Reminder{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE reminders SET status = 'cancelled', updated_at = ? WHERE id = ?`,
		stamp(s.now()), id); err != nil {
		return Reminder{}, fmt.Errorf("cancel reminder %d: %w", id, err)
	}
	s.changed()
	return s.Get(ctx, id)
}
