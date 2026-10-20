// Package jobs runs Hekate's scheduled background work (reflection, heartbeat,
// briefing) and records each run, so a missed run is caught up after a restart
// and she can see how her routines went.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/store"
)

// Statuses of a run.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusFailed  = "failed"
)

// ErrSkip from a job's Run means there was nothing to do (outside waking hours,
// nothing new): the run isn't recorded.
var ErrSkip = errors.New("nothing to do")

const (
	defaultTimeout = 10 * time.Minute
	// idleRecheck bounds how long a job sleeps: a disabled job, or one whose
	// settings changed without a Wake, is looked at again after this.
	idleRecheck = time.Hour
	maxSummary  = 600
	keepRuns    = 90 * 24 * time.Hour
)

// NextFunc says when a job should run next, given the start of its last
// recorded run (zero if it never ran). ok false means it's switched off.
type NextFunc func(ctx context.Context, now, last time.Time) (next time.Time, ok bool)

// Job is one piece of scheduled work.
type Job struct {
	Name    string
	Next    NextFunc
	Run     func(ctx context.Context) (summary string, err error)
	Timeout time.Duration // 0: ten minutes
}

// Run is one recorded run of a job.
type Run struct {
	ID         string  `json:"id"`
	Job        string  `json:"job"`
	Status     string  `json:"status"`
	Summary    string  `json:"summary"`
	Error      string  `json:"error"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

// Options are the dependencies of a Runner.
type Options struct {
	Store  *store.Store
	Logger *slog.Logger
	Now    func() time.Time // tests replace the clock
	// Sleep waits for d or until wake fires or ctx ends; tests replace it.
	Sleep func(ctx context.Context, d time.Duration, wake <-chan struct{})
}

// Runner schedules jobs and records their runs.
type Runner struct {
	db    *sql.DB
	log   *slog.Logger
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration, wake <-chan struct{})

	mu   sync.Mutex
	jobs []Job
	wake map[string]chan struct{}
}

// New builds a runner. Add jobs, then Run.
func New(o Options) (*Runner, error) {
	if o.Store == nil {
		return nil, errors.New("jobs: Store is required")
	}
	r := &Runner{db: o.Store.DB(), log: o.Logger, now: o.Now, sleep: o.Sleep, wake: map[string]chan struct{}{}}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.sleep == nil {
		r.sleep = sleep
	}
	return r, nil
}

// Add registers a job. Call before Run.
func (r *Runner) Add(j Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, j)
	r.wake[j.Name] = make(chan struct{}, 1)
}

// Names lists the registered jobs.
func (r *Runner) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, len(r.jobs))
	for i, j := range r.jobs {
		names[i] = j.Name
	}
	return names
}

// Wake makes a job work out its next run again, e.g. after its settings changed.
func (r *Runner) Wake(name string) {
	r.mu.Lock()
	ch := r.wake[name]
	r.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run schedules every job until ctx ends. Runs cut off by a restart are marked failed first.
func (r *Runner) Run(ctx context.Context) {
	if _, err := r.db.ExecContext(ctx, `UPDATE job_runs SET status = ?, error = 'cut off by a restart', finished_at = ?
		WHERE status = ?`, StatusFailed, stamp(r.now()), StatusRunning); err != nil {
		r.log.Error("cannot close interrupted job runs", "error", err)
	}
	r.prune(ctx)
	r.mu.Lock()
	jobs := append([]Job(nil), r.jobs...)
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Go(func() { r.loop(ctx, j) })
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context, j Job) {
	r.mu.Lock()
	wake := r.wake[j.Name]
	r.mu.Unlock()
	last, err := r.lastStart(ctx, j.Name)
	if err != nil {
		r.log.Error("cannot read a job's last run", "job", j.Name, "error", err)
	}
	for ctx.Err() == nil {
		now := r.now()
		next, ok := j.Next(ctx, now, last)
		if !ok {
			r.sleep(ctx, idleRecheck, wake)
			continue
		}
		if wait := next.Sub(now); wait > 0 {
			r.sleep(ctx, min(wait, idleRecheck), wake)
			continue // look again: the time may have come, or the schedule changed
		}
		last = r.now()
		r.runOnce(ctx, j)
	}
}

// RunNow runs a job immediately, outside its schedule, and records it like any
// other run. It is for tests and for the owner asking for a run.
func (r *Runner) RunNow(ctx context.Context, name string) error {
	r.mu.Lock()
	var job *Job
	for i := range r.jobs {
		if r.jobs[i].Name == name {
			job = &r.jobs[i]
		}
	}
	r.mu.Unlock()
	if job == nil {
		return fmt.Errorf("no job named %q", name)
	}
	r.runOnce(ctx, *job)
	return nil
}

func (r *Runner) runOnce(ctx context.Context, j Job) {
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	began := r.now()
	id, err := r.begin(ctx, j.Name, began)
	if err != nil {
		r.log.Error("cannot record a job run", "job", j.Name, "error", err)
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	summary, runErr := j.Run(rctx)
	cancel()
	switch {
	case errors.Is(runErr, ErrSkip):
		r.forget(ctx, id)
		return
	case runErr != nil:
		r.log.Warn("job failed", "job", j.Name, "error", runErr, "ms", time.Since(began).Milliseconds())
	default:
		r.log.Info("job done", "job", j.Name, "summary", clip(summary, 200), "ms", time.Since(began).Milliseconds())
	}
	r.finish(ctx, id, summary, runErr)
	r.prune(ctx)
}

// prune drops runs older than keepRuns; it runs at start and after every run.
func (r *Runner) prune(ctx context.Context) {
	if _, err := r.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM job_runs WHERE started_at < ?`, stamp(r.now().Add(-keepRuns))); err != nil {
		r.log.Warn("cannot prune old job runs", "error", err)
	}
}

func (r *Runner) begin(ctx context.Context, job string, at time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `INSERT INTO job_runs (job, status, started_at) VALUES (?, ?, ?)`, job, StatusRunning, stamp(at))
	if err != nil {
		return 0, fmt.Errorf("begin job run: %w", err)
	}
	return res.LastInsertId()
}

func (r *Runner) forget(ctx context.Context, id int64) {
	if id == 0 {
		return
	}
	if _, err := r.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM job_runs WHERE id = ?`, id); err != nil {
		r.log.Error("cannot drop a skipped job run", "error", err)
	}
}

func (r *Runner) finish(ctx context.Context, id int64, summary string, runErr error) {
	if id == 0 {
		return
	}
	status, msg := StatusOK, ""
	if runErr != nil {
		status, msg = StatusFailed, clip(oneLine(runErr.Error()), maxSummary)
	}
	_, err := r.db.ExecContext(context.WithoutCancel(ctx), `UPDATE job_runs SET status = ?, summary = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, clip(summary, maxSummary), msg, stamp(r.now()), id)
	if err != nil {
		r.log.Error("cannot finish a job run", "error", err)
	}
}

// lastStart is when the job's newest recorded run began (zero if never). A
// run cut off by a restart counts: retrying it in a loop helps nobody.
func (r *Runner) lastStart(ctx context.Context, job string) (time.Time, error) {
	var s sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM job_runs WHERE job = ?`, job).Scan(&s)
	if err != nil || !s.Valid {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, s.String)
}

// Latest returns each job's newest run (nil if it never ran), in registration order.
func (r *Runner) Latest(ctx context.Context) (map[string]*Run, error) {
	out := map[string]*Run{}
	for _, name := range r.Names() {
		runs, err := r.Runs(ctx, name, 1)
		if err != nil {
			return nil, err
		}
		if len(runs) > 0 {
			out[name] = &runs[0]
		} else {
			out[name] = nil
		}
	}
	return out, nil
}

// Runs lists recent runs, newest first; an empty job means every job.
func (r *Runner) Runs(ctx context.Context, job string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, job, status, summary, error, started_at, finished_at FROM job_runs
		WHERE ? = '' OR job = ? ORDER BY started_at DESC, id DESC LIMIT ?`, job, job, limit)
	if err != nil {
		return nil, fmt.Errorf("list job runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		var run Run
		var id int64
		var fin sql.NullString
		if err := rows.Scan(&id, &run.Job, &run.Status, &run.Summary, &run.Error, &run.StartedAt, &fin); err != nil {
			return nil, fmt.Errorf("scan job run: %w", err)
		}
		run.ID = fmt.Sprint(id)
		if fin.Valid {
			run.FinishedAt = &fin.String
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Clock is a time of day.
type Clock struct{ Hour, Minute int }

// ParseClock reads "HH:MM".
func ParseClock(s string) (Clock, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return Clock{}, fmt.Errorf("time of day %q: want HH:MM", s)
	}
	return Clock{t.Hour(), t.Minute()}, nil
}

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// On is the clock's time on t's day, in loc.
func (c Clock) On(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), c.Hour, c.Minute, 0, 0, loc)
}

// Daily runs once a day at the time at returns (ok false: switched off), in
// loc. A run missed while the service was down is caught up when it comes back
// within catchUp of the scheduled time; later than that, it waits for the next day.
func Daily(loc *time.Location, catchUp time.Duration, at func(ctx context.Context) (Clock, bool)) NextFunc {
	return func(ctx context.Context, now, last time.Time) (time.Time, bool) {
		c, ok := at(ctx)
		if !ok {
			return time.Time{}, false
		}
		t := c.On(now, loc)
		if now.Before(t) {
			// Today's run is still ahead. Yesterday's may have been missed just before
			// midnight: catch it up if it's recent enough and never ran.
			n := now.In(loc)
			y := c.On(time.Date(n.Year(), n.Month(), n.Day()-1, 12, 0, 0, 0, loc), loc)
			if !last.IsZero() && last.Before(y) && now.Sub(y) <= catchUp {
				return y, true
			}
			return t, true
		}
		if (!last.IsZero() && !last.Before(t)) || now.Sub(t) > catchUp {
			n := now.In(loc)
			return c.On(time.Date(n.Year(), n.Month(), n.Day()+1, 12, 0, 0, 0, loc), loc), true
		}
		return t, true
	}
}

// Every runs every interval after the last run; it's due at once when it never ran.
func Every(interval time.Duration) NextFunc {
	return func(_ context.Context, now, last time.Time) (time.Time, bool) {
		if last.IsZero() {
			return now, true
		}
		return last.Add(interval), true
	}
}

func sleep(ctx context.Context, d time.Duration, wake <-chan struct{}) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-wake:
	}
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
