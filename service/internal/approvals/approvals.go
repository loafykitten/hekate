package approvals

import (
	"context"
	"database/sql"
	"encoding/json"
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
)

// Limits.
const (
	expiry       = 24 * time.Hour
	execTimeout  = 2 * time.Minute
	recentWindow = 7 * 24 * time.Hour
	maxRows      = 200
	maxSleep     = time.Minute // re-check regularly in case the wall clock jumps
	retryAfter   = 5 * time.Second
	maxResult    = 500
	notifyTitle  = "Approval needed"
	// interruptedResult is recorded for an action cut off by a restart.
	interruptedResult = "Interrupted by a restart before it finished; it may have partly happened."
	deliverTimout     = 30 * time.Second
)

// Pusher delivers web push; push.Service satisfies it.
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Broadcaster sends a message to every connected client; server.Hub satisfies it.
type Broadcaster interface {
	Broadcast(msg any)
}

// Options are the dependencies of a Service.
type Options struct {
	Store    *store.Store
	Location *time.Location
	Events   proto.Events
	// Broadcast carries the approval event. Nil: clients are only told to refetch.
	Broadcast Broadcaster
	Push      Pusher // nil: in-app notifications only
	Logger    *slog.Logger
	// Now and After are the clock. Tests replace them; After(d) must behave
	// like time.After.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
	// ExecTimeout bounds one executor run; default 2 minutes.
	ExecTimeout time.Duration
}

// Service owns approvals, standing rules and the expiry loop.
type Service struct {
	db          *sql.DB
	loc         *time.Location
	events      proto.Events
	bcast       Broadcaster
	pusher      Pusher
	log         *slog.Logger
	now         func() time.Time
	after       func(time.Duration) <-chan time.Time
	execTimeout time.Duration

	mu        sync.Mutex // guards executors
	executors map[string]Executor
	wake      chan struct{} // tells the expiry loop a new approval is pending
}

// New builds the service. Call Run to start expiring approvals.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("approvals: Store is required")
	}
	s := &Service{db: o.Store.DB(), loc: o.Location, events: o.Events, bcast: o.Broadcast, pusher: o.Push,
		log: o.Logger, now: o.Now, after: o.After, execTimeout: o.ExecTimeout,
		executors: map[string]Executor{}, wake: make(chan struct{}, 1)}
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
	if s.execTimeout <= 0 {
		s.execTimeout = execTimeout
	}
	return s, nil
}

// Register sets the Executor for kind. Call it at startup, before requests.
func (s *Service) Register(kind string, exec Executor) error {
	if kind == "" || exec == nil {
		return errors.New("approvals: register needs a kind and an executor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.executors[kind]; dup {
		return fmt.Errorf("approvals: kind %q is already registered", kind)
	}
	s.executors[kind] = exec
	return nil
}

func (s *Service) executor(kind string) (Executor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.executors[kind]
	return e, ok
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

func (s *Service) displayPtr(stored sql.NullString) *string {
	if !stored.Valid {
		return nil
	}
	v := s.display(stored.String)
	return &v
}

// ErrNotPending is returned by Decide when the approval was already decided or
// has expired. It is a 409 over REST.
var ErrNotPending = httpx.Conflict("this approval is no longer pending")

const columns = `id, kind, title, detail, scope_label, status, result, decided_by, created_at, expires_at, decided_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row scanner) (Approval, error) {
	var (
		a                       Approval
		id                      int64
		scopeLabel              string
		result, decidedBy, when sql.NullString
		created, expires        string
	)
	if err := row.Scan(&id, &a.Kind, &a.Title, &a.Detail, &scopeLabel, &a.Status, &result, &decidedBy, &created, &expires, &when); err != nil {
		return Approval{}, err //nolint:wrapcheck // callers distinguish sql.ErrNoRows
	}
	a.ID = strconv.FormatInt(id, 10)
	if scopeLabel != "" {
		a.ScopeLabel = &scopeLabel
	}
	if result.Valid {
		a.Result = &result.String
	}
	if decidedBy.Valid {
		a.DecidedBy = &decidedBy.String
	}
	a.CreatedAt, a.ExpiresAt, a.DecidedAt = s.display(created), s.display(expires), s.displayPtr(when)
	return a, nil
}

func notFound(id int64) error { return httpx.NotFound("there is no approval with id %d", id) }

// Get returns one approval. A pending one past its expiry is expired first.
func (s *Service) Get(ctx context.Context, id int64) (Approval, error) {
	if err := s.expireDue(ctx); err != nil {
		return Approval{}, err
	}
	return s.get(ctx, id)
}

func (s *Service) get(ctx context.Context, id int64) (Approval, error) {
	a, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM approvals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, notFound(id)
	}
	if err != nil {
		return Approval{}, fmt.Errorf("get approval %d: %w", id, err)
	}
	return a, nil
}

// List filters.
const (
	ListPending = "pending"
	ListRecent  = "recent"
)

// List returns pending approvals (the default, oldest first) or, for
// ListRecent, everything decided or expired in the last 7 days, newest first.
func (s *Service) List(ctx context.Context, which string) ([]Approval, error) {
	var (
		q    string
		args []any
	)
	switch which {
	case "", ListPending:
		q = `SELECT ` + columns + ` FROM approvals WHERE status = 'pending' ORDER BY created_at, id LIMIT ?`
		args = []any{maxRows}
	case ListRecent:
		q = `SELECT ` + columns + ` FROM approvals WHERE status <> 'pending' AND decided_at >= ? ORDER BY decided_at DESC, id DESC LIMIT ?`
		args = []any{stamp(s.now().Add(-recentWindow)), maxRows}
	default:
		return nil, httpx.BadRequest("status must be pending or recent")
	}
	if err := s.expireDue(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Approval{}
	for rows.Next() {
		a, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list approvals: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	return out, nil
}

// Card is the approval card for a.
func Card(a Approval) proto.Card {
	b, _ := json.Marshal(a) // plain struct of strings: cannot fail
	return proto.Card{Kind: "approval", ID: "approval-" + a.ID, Title: "Approval", Data: b}
}

// publish tells clients an approval was created or changed.
func (s *Service) publish(a Approval) {
	if s.bcast != nil {
		b, _ := json.Marshal(a) // plain struct of strings: cannot fail
		s.bcast.Broadcast(proto.NewApproval(b))
	}
	s.events.Invalidate(proto.DomainApprovals)
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Request asks the owner to allow an action, or runs it at once when a
// standing rule covers its scope.
func (s *Service) Request(ctx context.Context, r Request) (Approval, error) {
	exec, ok := s.executor(r.Kind)
	if !ok {
		return Approval{}, fmt.Errorf("approvals: no executor registered for kind %q", r.Kind)
	}
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return Approval{}, errors.New("approvals: a request needs a title")
	}
	payload := string(r.Payload)
	if payload == "" {
		payload = "null"
	}
	if r.Scope == "" {
		r.ScopeLabel = "" // no rule can cover it
	}
	now := s.now()

	if r.Scope != "" {
		res, err := s.db.ExecContext(ctx, `UPDATE approval_rules SET uses = uses + 1 WHERE kind = ? AND scope = ?`, r.Kind, r.Scope)
		if err != nil {
			return Approval{}, fmt.Errorf("look up rule: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return s.runByRule(ctx, exec, r, payload, now)
		}
	}

	res, err := s.db.ExecContext(ctx, `INSERT INTO approvals (kind, title, detail, scope, scope_label, payload, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		r.Kind, r.Title, r.Detail, r.Scope, r.ScopeLabel, payload, stamp(now), stamp(now.Add(expiry)))
	if err != nil {
		return Approval{}, fmt.Errorf("insert approval: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Approval{}, fmt.Errorf("insert approval: %w", err)
	}
	a, err := s.get(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	s.poke()
	s.publish(a)
	if r.Notify {
		s.notify(ctx, a)
	}
	return a, nil
}

// runByRule runs a request a standing rule covers and records it as decided.
func (s *Service) runByRule(ctx context.Context, exec Executor, r Request, payload string, now time.Time) (Approval, error) {
	status, result := s.run(ctx, exec, r.Payload)
	res, err := s.db.ExecContext(ctx, `INSERT INTO approvals (kind, title, detail, scope, scope_label, payload, status, result, decided_by, created_at, expires_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'rule', ?, ?, ?)`,
		r.Kind, r.Title, r.Detail, r.Scope, r.ScopeLabel, payload, status, result, stamp(now), stamp(now.Add(expiry)), stamp(s.now()))
	if err != nil {
		return Approval{}, fmt.Errorf("record approval run by rule: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Approval{}, fmt.Errorf("record approval run by rule: %w", err)
	}
	a, err := s.get(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	s.publish(a)
	return a, nil
}

// run executes an action on a context that outlives the caller (an app closed
// mid-tap must not abort it halfway) and returns the status and result to record.
func (s *Service) run(ctx context.Context, exec Executor, payload json.RawMessage) (status, result string) {
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.execTimeout)
	defer cancel()
	out, err := safeExec(ectx, exec, payload)
	if err != nil {
		return StatusFailed, clip(err.Error())
	}
	return StatusDone, clip(out)
}

func safeExec(ctx context.Context, exec Executor, payload json.RawMessage) (out string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("the action crashed: %v", rec)
		}
	}()
	return exec(ctx, payload)
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxResult {
		return s
	}
	return string([]rune(s)[:maxResult-1]) + "…"
}

// Decide records the owner's decision on a pending approval and, unless it is
// a denial, runs the action. The claim is a single guarded UPDATE, so two
// devices or a double tap can never run an action twice; the loser gets
// ErrNotPending.
func (s *Service) Decide(ctx context.Context, id int64, decision string) (Approval, error) {
	switch decision {
	case DecisionOnce, DecisionAlways, DecisionDeny:
	default:
		return Approval{}, httpx.BadRequest("decision must be once, always or deny")
	}
	var scope, scopeLabel, payload string
	err := s.db.QueryRowContext(ctx, `SELECT scope, scope_label, payload FROM approvals WHERE id = ?`, id).Scan(&scope, &scopeLabel, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, notFound(id)
	}
	if err != nil {
		return Approval{}, fmt.Errorf("load approval %d: %w", id, err)
	}
	if decision == DecisionAlways && scope == "" {
		return Approval{}, httpx.BadRequest("this approval can't become a standing rule")
	}

	now := s.now()
	// Denying settles it; allowing claims it as running and records the outcome
	// below.
	claimed := StatusRunning
	if decision == DecisionDeny {
		claimed = StatusDenied
	}
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = ?, decided_by = 'owner', decided_at = ?
		WHERE id = ? AND status = 'pending' AND expires_at > ?`, claimed, stamp(now), id, stamp(now))
	if err != nil {
		return Approval{}, fmt.Errorf("claim approval %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Approval{}, ErrNotPending
	}

	// Past the claim the action and its record finish even if the caller leaves.
	ctx = context.WithoutCancel(ctx)
	if decision != DecisionDeny {
		a, err := s.get(ctx, id)
		if err != nil {
			return Approval{}, err
		}
		s.publish(a) // other devices show it as running
		if decision == DecisionAlways {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO approval_rules (kind, scope, scope_label, created_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (kind, scope) DO NOTHING`, a.Kind, scope, scopeLabel, stamp(now)); err != nil {
				s.log.Error("cannot store approval rule", "id", id, "error", err)
			} else {
				s.events.Invalidate(proto.DomainApprovals)
			}
		}
		status, result := StatusFailed, "no action is registered for "+a.Kind
		if exec, ok := s.executor(a.Kind); ok {
			status, result = s.run(ctx, exec, json.RawMessage(payload))
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = ?, result = ? WHERE id = ? AND status = 'running'`, status, result, id); err != nil {
			return Approval{}, fmt.Errorf("record approval %d result: %w", id, err)
		}
	}
	a, err := s.get(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	s.publish(a)
	return a, nil
}

// notify tells the owner by notify event and push that an approval waits.
func (s *Service) notify(ctx context.Context, a Approval) {
	card := Card(a)
	s.events.Notify(proto.NewNotify("approval-"+a.ID, proto.NotifyKindApproval, notifyTitle, a.Title, &card))
	if s.pusher == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliverTimout)
	defer cancel()
	if _, err := s.pusher.Send(pctx, push.Payload{Title: notifyTitle, Body: a.Title, URL: "/", Tag: "approval-" + a.ID}, push.UrgencyHigh); err != nil {
		s.log.Error("cannot send push for approval", "id", a.ID, "error", err)
	}
}

// expireDue turns pending approvals past their expiry into expired ones.
func (s *Service) expireDue(ctx context.Context) error {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM approvals WHERE status = 'pending' AND expires_at <= ? ORDER BY id`, stamp(now))
	if err != nil {
		return fmt.Errorf("find expired approvals: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("find expired approvals: %w", err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("find expired approvals: %w", err)
	}
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = 'expired', decided_at = ?
			WHERE id = ? AND status = 'pending' AND expires_at <= ?`, stamp(now), id, stamp(now))
		if err != nil {
			return fmt.Errorf("expire approval %d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // decided under us
		}
		if a, err := s.get(ctx, id); err == nil {
			s.publish(a)
		}
	}
	return nil
}

// failInterrupted settles actions that were still running when the service
// last stopped. Run calls it once at startup, before any new decision.
func (s *Service) failInterrupted(ctx context.Context) error {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = 'failed', result = ? WHERE status = 'running'`, interruptedResult)
	if err != nil {
		return fmt.Errorf("settle interrupted approvals: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.log.Warn("approvals were interrupted by a restart", "count", n)
		s.events.Invalidate(proto.DomainApprovals)
	}
	return nil
}

// nextExpiry is when the earliest pending approval expires.
func (s *Service) nextExpiry(ctx context.Context) (time.Time, bool, error) {
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM approvals WHERE status = 'pending' ORDER BY expires_at LIMIT 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("next expiry: %w", err)
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("next expiry %q: %w", at, err)
	}
	return t, true, nil
}

// Run expires approvals on time until ctx is done. It sleeps until the
// earliest expiry (at most a minute) and wakes early on a new request.
func (s *Service) Run(ctx context.Context) {
	if err := s.failInterrupted(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("cannot settle approvals interrupted by a restart", "error", err)
	}
	for ctx.Err() == nil {
		wait := maxSleep
		if err := s.expireDue(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Error("cannot expire approvals", "error", err)
			wait = retryAfter
		} else if next, ok, err := s.nextExpiry(ctx); err != nil {
			s.log.Error("cannot read the next approval expiry", "error", err)
			wait = retryAfter
		} else if ok {
			wait = max(min(next.Sub(s.now()), maxSleep), 0)
			if wait == 0 {
				continue
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

// Rules returns the standing rules, newest first.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, scope_label, uses, created_at FROM approval_rules ORDER BY id DESC LIMIT ?`, maxRows)
	if err != nil {
		return nil, fmt.Errorf("list approval rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Rule{}
	for rows.Next() {
		var (
			r       Rule
			id      int64
			created string
		)
		if err := rows.Scan(&id, &r.Kind, &r.ScopeLabel, &r.Uses, &created); err != nil {
			return nil, fmt.Errorf("list approval rules: %w", err)
		}
		r.ID, r.CreatedAt = strconv.FormatInt(id, 10), s.display(created)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list approval rules: %w", err)
	}
	return out, nil
}

// DeleteRule removes a standing rule; later matching requests ask again.
func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM approval_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete approval rule %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.NotFound("there is no approval rule with id %d", id)
	}
	s.events.Invalidate(proto.DomainApprovals)
	return nil
}
