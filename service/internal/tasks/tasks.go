// Package tasks is the to-do list: named lists, due dates, repeating tasks
// and the REST and tool surfaces over them.
package tasks

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
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/when"
)

// DefaultList is where a task lands when no list is given.
const DefaultList = "Inbox"

// Field limits.
const (
	maxTitle = 300
	maxNotes = 4000
	maxList  = 60
	maxRows  = 500
)

// Task is the API shape from PROTOCOL.md.
type Task struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Notes       string  `json:"notes"`
	List        string  `json:"list"`
	Due         *string `json:"due"`
	RRule       *string `json:"rrule"`
	Priority    int     `json:"priority"`
	CompletedAt *string `json:"completed_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Status filters tasks by completion.
type Status string

// Task statuses.
const (
	StatusOpen Status = "open"
	StatusDone Status = "done"
	StatusAll  Status = "all"
)

// NewTask is the input for Create. Empty strings mean "not given".
type NewTask struct {
	Title    string
	Notes    string
	List     string
	Due      string
	RRule    string
	Priority int
}

// Patch changes a task. Nil fields are left alone; for Due and RRule the empty
// string clears the value.
type Patch struct {
	Title    *string
	Notes    *string
	List     *string
	Due      *string
	RRule    *string
	Priority *int
	Done     *bool // true completes the task, false reopens it
}

// Filter selects tasks. The zero value lists open tasks of every list.
type Filter struct {
	List      string
	Status    Status
	DueBefore *when.Value // exclusive; a date means "before that day starts"
	Limit     int
}

// ListInfo is one entry of GET /api/lists.
type ListInfo struct {
	Name      string `json:"name"`
	OpenCount int    `json:"open_count"`
}

// Options are the dependencies of a Service.
type Options struct {
	Store    *store.Store
	Location *time.Location
	Events   proto.Events
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service owns task data.
type Service struct {
	db     *sql.DB
	loc    *time.Location
	events proto.Events
	log    *slog.Logger
	now    func() time.Time

	// mu serializes read-modify-write sequences; SQLite would otherwise fail
	// one of two overlapping transactions with SQLITE_BUSY.
	mu sync.Mutex
}

// New builds the service.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("tasks: Store is required")
	}
	s := &Service{db: o.Store.DB(), loc: o.Location, events: o.Events, log: o.Logger, now: o.Now}
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
	return s, nil
}

const columns = `id, title, notes, list, due, rrule, priority, completed_at, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row scanner) (Task, error) {
	var (
		t                    Task
		id                   int64
		due, rrule, complete sql.NullString
		created, updated     string
	)
	if err := row.Scan(&id, &t.Title, &t.Notes, &t.List, &due, &rrule, &t.Priority, &complete, &created, &updated); err != nil {
		return Task{}, err //nolint:wrapcheck // callers distinguish sql.ErrNoRows
	}
	t.ID = strconv.FormatInt(id, 10)
	t.Due, t.RRule = nullable(due), nullable(rrule)
	if complete.Valid {
		c := s.display(complete.String)
		t.CompletedAt = &c
	}
	t.CreatedAt, t.UpdatedAt = s.display(created), s.display(updated)
	return t, nil
}

func nullable(n sql.NullString) *string {
	if !n.Valid || n.String == "" {
		return nil
	}
	return &n.String
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// display converts a stored stamp to RFC 3339 in the configured zone.
func (s *Service) display(stored string) string {
	t, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		return stored
	}
	return t.In(s.loc).Format(time.RFC3339)
}

func notFound(id int64) error { return httpx.NotFound("there is no task with id %d", id) }

func (s *Service) get(ctx context.Context, id int64) (Task, error) {
	t, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM tasks WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, notFound(id)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task %d: %w", id, err)
	}
	return t, nil
}

// Get returns one task.
func (s *Service) Get(ctx context.Context, id int64) (Task, error) { return s.get(ctx, id) }

// List returns tasks matching f: open ones by due date (undated last) then
// priority, finished ones newest first.
func (s *Service) List(ctx context.Context, f Filter) ([]Task, error) {
	var (
		where = []string{"deleted_at IS NULL"}
		args  []any
	)
	switch f.Status {
	case StatusDone:
		where = append(where, "completed_at IS NOT NULL")
	case StatusAll:
	case StatusOpen, "":
		where = append(where, "completed_at IS NULL")
	default:
		return nil, httpx.BadRequest("status must be open, done or all")
	}
	if l := strings.TrimSpace(f.List); l != "" {
		where = append(where, "list = ?") // the column is NOCASE
		args = append(args, l)
	}
	if f.DueBefore != nil {
		where = append(where, "due_at < ?")
		args = append(args, f.DueBefore.T.Unix())
	}
	order := "due_at IS NULL, due_at, priority DESC, id"
	if f.Status == StatusDone {
		order = "completed_at DESC, id DESC"
	}
	limit := f.Limit
	if limit <= 0 || limit > maxRows {
		limit = maxRows
	}
	// The fragments joined here are fixed strings; every value is bound.
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM tasks WHERE `+strings.Join(where, " AND ")+ //nolint:gosec // see above
		` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Task{}
	for rows.Next() {
		t, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return out, nil
}

// Lists returns every list that has a task, with its open count.
func (s *Service) Lists(ctx context.Context) ([]ListInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT MIN(list), COALESCE(SUM(completed_at IS NULL), 0)
		FROM tasks WHERE deleted_at IS NULL GROUP BY list ORDER BY list`)
	if err != nil {
		return nil, fmt.Errorf("list lists: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ListInfo{}
	for rows.Next() {
		var l ListInfo
		if err := rows.Scan(&l.Name, &l.OpenCount); err != nil {
			return nil, fmt.Errorf("list lists: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list lists: %w", err)
	}
	return out, nil
}

// canonicalList returns the spelling an existing list already uses, so
// "groceries" joins "Groceries" instead of starting a second list.
func (s *Service) canonicalList(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultList
	}
	if utf8.RuneCountInString(name) > maxList {
		return "", httpx.BadRequest("list name is longer than %d characters", maxList)
	}
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT list FROM tasks WHERE list = ? AND deleted_at IS NULL ORDER BY id LIMIT 1`, name).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return name, nil
	case err != nil:
		return "", fmt.Errorf("find list: %w", err)
	}
	return existing, nil
}

// dueFields validates a due string into its stored forms.
func (s *Service) dueFields(due string) (v when.Value, err error) {
	v, err = when.Parse(due, s.loc)
	if err != nil {
		return v, httpx.BadRequest("due: %s", err)
	}
	return v, nil
}

func checkText(field, v string, limit int) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > limit {
		return "", httpx.BadRequest("%s is longer than %d characters", field, limit)
	}
	return v, nil
}

// Create adds a task.
func (s *Service) Create(ctx context.Context, n NewTask) (Task, error) {
	t, err := s.create(ctx, n)
	if err != nil {
		return Task{}, err
	}
	s.events.Invalidate(proto.DomainTasks)
	return t, nil
}

func (s *Service) create(ctx context.Context, n NewTask) (Task, error) {
	title, err := checkText("title", n.Title, maxTitle)
	if err != nil {
		return Task{}, err
	}
	if title == "" {
		return Task{}, httpx.BadRequest("title is required")
	}
	notes, err := checkText("notes", n.Notes, maxNotes)
	if err != nil {
		return Task{}, err
	}
	if n.Priority < 0 || n.Priority > 3 {
		return Task{}, httpx.BadRequest("priority must be 0 (none) to 3 (high)")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.canonicalList(ctx, n.List)
	if err != nil {
		return Task{}, err
	}
	now := s.now()

	var (
		due     *when.Value
		ruleStr string
	)
	if strings.TrimSpace(n.Due) != "" {
		v, err := s.dueFields(n.Due)
		if err != nil {
			return Task{}, err
		}
		due = &v
	}
	if strings.TrimSpace(n.RRule) != "" {
		due, ruleStr, err = s.withRule(due, n.RRule, now)
		if err != nil {
			return Task{}, err
		}
	}

	var dueStr, dueAt, rule any
	if due != nil {
		dueStr, dueAt = due.String(), due.T.Unix()
	}
	if ruleStr != "" {
		rule = ruleStr
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tasks (title, notes, list, due, due_at, rrule, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, title, notes, list, dueStr, dueAt, rule, n.Priority, stamp(now), stamp(now))
	if err != nil {
		return Task{}, fmt.Errorf("insert task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Task{}, fmt.Errorf("insert task: %w", err)
	}
	return s.get(ctx, id)
}

// withRule validates rule against due and returns the due to store. A repeating
// task with no due starts at the first occurrence on or after today.
func (s *Service) withRule(due *when.Value, rule string, now time.Time) (*when.Value, string, error) {
	start := when.StartOfDay(now, s.loc)
	if due != nil {
		start = due.T
	}
	r, err := when.ParseRule(rule, start, s.loc)
	if err != nil {
		return nil, "", httpx.BadRequest("rrule: %s", err)
	}
	timed := ruleHasTimeOfDay(rule)
	if due != nil && due.DateOnly && timed {
		// A date can't hold the rule's time, so completing would land on the same
		// occurrence again.
		return nil, "", httpx.BadRequest("rrule: a rule with times of day needs a due time too (e.g. 2026-10-20T09:00)")
	}
	if due == nil {
		first, ok := r.First(start)
		if !ok {
			return nil, "", httpx.BadRequest("rrule: the rule has no occurrences from today on")
		}
		due = &when.Value{T: first, DateOnly: !timed}
	}
	return due, r.String(), nil
}

// ruleHasTimeOfDay reports whether an RRULE picks times within a day (hourly,
// or BYHOUR/BYMINUTE/BYSECOND), which a date-only due can't represent.
func ruleHasTimeOfDay(rule string) bool {
	for _, part := range strings.Split(strings.ToUpper(rule), ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "BYHOUR", "BYMINUTE", "BYSECOND":
			return true
		case "FREQ":
			if v == "HOURLY" || v == "MINUTELY" || v == "SECONDLY" {
				return true
			}
		}
	}
	return false
}

// Update changes a task.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (Task, error) {
	s.mu.Lock()
	t, err := s.update(ctx, id, p)
	s.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	s.events.Invalidate(proto.DomainTasks)
	return t, nil
}

func (s *Service) update(ctx context.Context, id int64, p Patch) (Task, error) {
	cur, err := s.get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	title, notes, list := cur.Title, cur.Notes, cur.List
	priority := cur.Priority
	if p.Title != nil {
		if title, err = checkText("title", *p.Title, maxTitle); err != nil {
			return Task{}, err
		}
		if title == "" {
			return Task{}, httpx.BadRequest("title cannot be empty")
		}
	}
	if p.Notes != nil {
		if notes, err = checkText("notes", *p.Notes, maxNotes); err != nil {
			return Task{}, err
		}
	}
	if p.List != nil {
		if list, err = s.canonicalList(ctx, *p.List); err != nil {
			return Task{}, err
		}
	}
	if p.Priority != nil {
		if *p.Priority < 0 || *p.Priority > 3 {
			return Task{}, httpx.BadRequest("priority must be 0 (none) to 3 (high)")
		}
		priority = *p.Priority
	}

	now := s.now()
	var due *when.Value
	if cur.Due != nil {
		v, err := when.Parse(*cur.Due, s.loc)
		if err == nil {
			due = &v
		}
	}
	ruleStr := ""
	if cur.RRule != nil {
		ruleStr = *cur.RRule
	}
	if p.Due != nil {
		due = nil
		if strings.TrimSpace(*p.Due) != "" {
			v, err := s.dueFields(*p.Due)
			if err != nil {
				return Task{}, err
			}
			due = &v
		}
	}
	if p.RRule != nil {
		ruleStr = strings.TrimSpace(*p.RRule)
	}
	if ruleStr != "" && (p.RRule != nil || p.Due != nil) {
		if due, ruleStr, err = s.withRule(due, ruleStr, now); err != nil {
			return Task{}, err
		}
	}

	var dueStr, dueAt, rule any
	if due != nil {
		dueStr, dueAt = due.String(), due.T.Unix()
	}
	if ruleStr != "" {
		rule = ruleStr
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET title = ?, notes = ?, list = ?, due = ?, due_at = ?, rrule = ?,
		priority = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		title, notes, list, dueStr, dueAt, rule, priority, stamp(now), id); err != nil {
		return Task{}, fmt.Errorf("update task %d: %w", id, err)
	}
	if p.Done != nil && *p.Done != (cur.CompletedAt != nil) {
		if *p.Done {
			return s.complete(ctx, id)
		}
		return s.reopen(ctx, id)
	}
	return s.get(ctx, id)
}

// Complete finishes a task. A repeating task records the completion and moves
// its due to the next occurrence instead of closing.
func (s *Service) Complete(ctx context.Context, id int64) (Task, error) {
	s.mu.Lock()
	t, err := s.complete(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	s.events.Invalidate(proto.DomainTasks)
	return t, nil
}

func (s *Service) complete(ctx context.Context, id int64) (Task, error) {
	cur, err := s.get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if cur.CompletedAt != nil {
		return cur, nil // already done
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("complete task %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	closed := true // false when a repeating task moves on instead
	if cur.RRule != nil {
		next, rule, ok, err := s.nextDue(cur, now)
		if err != nil {
			return Task{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_completions (task_id, due, completed_at) VALUES (?, ?, ?)`,
			id, cur.Due, stamp(now)); err != nil {
			return Task{}, fmt.Errorf("record completion: %w", err)
		}
		if ok {
			closed = false
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET due = ?, due_at = ?, rrule = ?, updated_at = ? WHERE id = ?`,
				next.String(), next.T.Unix(), rule, stamp(now), id); err != nil {
				return Task{}, fmt.Errorf("move due: %w", err)
			}
		}
	}
	if closed {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET completed_at = ?, updated_at = ? WHERE id = ?`,
			stamp(now), stamp(now), id); err != nil {
			return Task{}, fmt.Errorf("complete task %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("complete task %d: %w", id, err)
	}
	return s.get(ctx, id)
}

// nextDue is the occurrence after the current due, or after today when the
// task is overdue, so finishing a stale chore does not leave it overdue again.
// It also returns the rule to store with it (a COUNT limit gets spent).
func (s *Service) nextDue(t Task, now time.Time) (next when.Value, rule string, ok bool, err error) {
	var due when.Value
	if t.Due != nil {
		v, err := when.Parse(*t.Due, s.loc)
		if err != nil {
			return when.Value{}, "", false, fmt.Errorf("task %s has an unreadable due %q: %w", t.ID, *t.Due, err)
		}
		due = v
	} else {
		due = when.Value{T: when.StartOfDay(now, s.loc), DateOnly: true}
	}
	r, err := when.ParseRule(*t.RRule, due.T, s.loc)
	if err != nil {
		return when.Value{}, "", false, fmt.Errorf("task %s has an unreadable rule: %w", t.ID, err)
	}
	after := now
	if due.DateOnly {
		after = when.StartOfDay(now, s.loc) // today is not "after today"
	}
	if due.T.After(after) {
		after = due.T
	}
	n, ok := r.Next(after)
	if !ok {
		return when.Value{}, "", false, nil
	}
	return when.Value{T: n, DateOnly: due.DateOnly}, r.Rebase(n), true, nil
}

// Reopen marks a finished task open again.
func (s *Service) Reopen(ctx context.Context, id int64) (Task, error) {
	s.mu.Lock()
	t, err := s.reopen(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	s.events.Invalidate(proto.DomainTasks)
	return t, nil
}

func (s *Service) reopen(ctx context.Context, id int64) (Task, error) {
	if _, err := s.get(ctx, id); err != nil {
		return Task{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET completed_at = NULL, updated_at = ? WHERE id = ?`,
		stamp(s.now()), id); err != nil {
		return Task{}, fmt.Errorf("reopen task %d: %w", id, err)
	}
	return s.get(ctx, id)
}

// Delete removes a task from every view. The row stays, marked deleted.
func (s *Service) Delete(ctx context.Context, id int64) (Task, error) {
	s.mu.Lock()
	t, err := s.remove(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	s.events.Invalidate(proto.DomainTasks)
	return t, nil
}

func (s *Service) remove(ctx context.Context, id int64) (Task, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	now := stamp(s.now())
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET deleted_at = ?, updated_at = ? WHERE id = ?`, now, now, id); err != nil {
		return Task{}, fmt.Errorf("delete task %d: %w", id, err)
	}
	return t, nil
}
