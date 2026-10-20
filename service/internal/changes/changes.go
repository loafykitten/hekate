// Package changes holds the changes Renamon proposes to Hekate itself. Each
// proposal waits for the owner as an approval; once allowed, a worker on the
// development machine claims it over REST, builds it and reports back until
// it is merged, rejected or failed.
package changes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

// KindApproval is the approval kind of a proposal.
const KindApproval = "change_request"

// Statuses. Proposed, denied and expired come from the proposal's approval;
// the rest from the worker.
const (
	StatusProposed = "proposed"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
	StatusQueued   = "queued"
	StatusWorking  = "working"
	StatusMerged   = "merged"
	StatusRejected = "rejected"
	StatusFailed   = "failed"
)

var statuses = []string{StatusProposed, StatusDenied, StatusExpired, StatusQueued, StatusWorking, StatusMerged, StatusRejected, StatusFailed}

// Kinds of change.
var kinds = []string{"feature", "fix", "improvement"}

// Limits.
const (
	dailyCap      = 5
	capWindow     = 24 * time.Hour
	proposalTTL   = 7 * 24 * time.Hour
	listWindow    = 60 * 24 * time.Hour
	maxRows       = 200
	maxTitle      = 80
	maxWhat       = 2000
	maxWhy        = 800
	maxStage      = 120
	maxResult     = 600
	maxURL        = 300
	urlPrefix     = "https://github.com/"
	deliverTimout = 30 * time.Second
)

// ChangeRequest is the API shape from PROTOCOL.md.
type ChangeRequest struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	What       string  `json:"what"`
	Why        string  `json:"why"`
	Kind       string  `json:"kind"`
	Revises    *string `json:"revises"`
	Status     string  `json:"status"`
	ApprovalID *string `json:"approval_id"`
	Feedback   *string `json:"feedback"`
	Stage      string  `json:"stage"`
	IssueURL   *string `json:"issue_url"`
	PRURL      *string `json:"pr_url"`
	Result     *string `json:"result"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	FinishedAt *string `json:"finished_at"`
}

// Pusher delivers web push; push.Service satisfies it.
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Options are the dependencies of a Service.
type Options struct {
	Store     *store.Store
	Approvals *approvals.Service
	Location  *time.Location
	Events    proto.Events
	Push      Pusher // nil: in-app notifications only
	Logger    *slog.Logger
	Now       func() time.Time // tests replace the clock
}

// Service stores change requests and tracks them through the worker.
type Service struct {
	db     *sql.DB
	appr   *approvals.Service
	loc    *time.Location
	events proto.Events
	pusher Pusher
	log    *slog.Logger
	now    func() time.Time
}

// New builds the service and registers the proposal's approval kind.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Approvals == nil {
		return nil, errors.New("changes: Store and Approvals are required")
	}
	s := &Service{db: o.Store.DB(), appr: o.Approvals, loc: o.Location, events: o.Events, pusher: o.Push, log: o.Logger, now: o.Now}
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
	if err := o.Approvals.Register(KindApproval, s.queue); err != nil {
		return nil, fmt.Errorf("changes: %w", err)
	}
	return s, nil
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

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// statusExpr is a proposal's status: its approval's verdict until the owner
// allows it, the worker's state after.
const statusExpr = `CASE WHEN c.state <> 'proposed' THEN c.state
	WHEN a.status = 'denied' THEN 'denied'
	WHEN a.status = 'expired' THEN 'expired'
	WHEN a.status = 'failed' THEN 'failed'
	ELSE 'proposed' END`

const selectCols = `SELECT c.id, c.title, c.what, c.why, c.kind, c.revises, ` + statusExpr + `, c.approval_id,
	CASE WHEN a.status = 'denied' THEN a.note END, c.stage, c.issue_url, c.pr_url,
	COALESCE(c.result, CASE WHEN c.state = 'proposed' AND a.status = 'failed' THEN a.result END),
	c.created_at, c.updated_at, c.finished_at
	FROM change_requests c LEFT JOIN approvals a ON a.id = c.approval_id`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row scanner) (ChangeRequest, error) {
	var (
		c                                ChangeRequest
		id                               int64
		revises, approvalID              sql.NullInt64
		feedback, issue, pr, result, fin sql.NullString
		created, updated                 string
	)
	if err := row.Scan(&id, &c.Title, &c.What, &c.Why, &c.Kind, &revises, &c.Status, &approvalID,
		&feedback, &c.Stage, &issue, &pr, &result, &created, &updated, &fin); err != nil {
		return c, err //nolint:wrapcheck // callers distinguish sql.ErrNoRows
	}
	c.ID = strconv.FormatInt(id, 10)
	idPtr := func(n sql.NullInt64) *string {
		if !n.Valid {
			return nil
		}
		v := strconv.FormatInt(n.Int64, 10)
		return &v
	}
	strPtr := func(n sql.NullString) *string {
		if !n.Valid {
			return nil
		}
		return &n.String
	}
	c.Revises, c.ApprovalID = idPtr(revises), idPtr(approvalID)
	c.Feedback, c.IssueURL, c.PRURL, c.Result = strPtr(feedback), strPtr(issue), strPtr(pr), strPtr(result)
	c.CreatedAt, c.UpdatedAt = s.display(created), s.display(updated)
	if fin.Valid {
		v := s.display(fin.String)
		c.FinishedAt = &v
	}
	return c, nil
}

func notFound(id int64) error { return httpx.NotFound("there is no change request %d", id) }

// Get returns one change request.
func (s *Service) Get(ctx context.Context, id int64) (ChangeRequest, error) {
	c, err := s.scan(s.db.QueryRowContext(ctx, selectCols+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound(id)
	}
	if err != nil {
		return c, fmt.Errorf("get change request %d: %w", id, err)
	}
	return c, nil
}

// List returns change requests newest first: those with the given status, or
// with none given, everything from the last 60 days.
func (s *Service) List(ctx context.Context, status string) ([]ChangeRequest, error) {
	q, args := selectCols+` WHERE c.created_at >= ?`, []any{stamp(s.now().Add(-listWindow))}
	if status != "" {
		if !slices.Contains(statuses, status) {
			return nil, httpx.BadRequest("status must be one of %s", strings.Join(statuses, ", "))
		}
		q, args = selectCols+` WHERE `+statusExpr+` = ?`, []any{status}
	}
	// Approvals expire lazily: let them settle so a stale proposal reads expired.
	if _, err := s.appr.List(ctx, approvals.ListPending); err != nil {
		return nil, fmt.Errorf("list change requests: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY c.id DESC LIMIT ?`, append(args, maxRows)...)
	if err != nil {
		return nil, fmt.Errorf("list change requests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ChangeRequest{}
	for rows.Next() {
		c, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list change requests: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list change requests: %w", err)
	}
	return out, nil
}

// Proposal is what Renamon proposes.
type Proposal struct {
	Title   string `json:"title"`
	What    string `json:"what"`
	Why     string `json:"why"`
	Kind    string `json:"kind"`
	Revises int64  `json:"revises,omitempty"`
}

func (p Proposal) normalize() (Proposal, error) {
	p.Title = strings.Join(strings.Fields(p.Title), " ")
	p.What, p.Why = strings.TrimSpace(p.What), strings.TrimSpace(p.Why)
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	for _, f := range []struct {
		name, v string
		max     int
	}{{"title", p.Title, maxTitle}, {"what", p.What, maxWhat}, {"why", p.Why, maxWhy}} {
		if f.v == "" {
			return p, httpx.BadRequest("%s is required", f.name)
		}
		if utf8.RuneCountInString(f.v) > f.max {
			return p, httpx.BadRequest("%s can be at most %d characters", f.name, f.max)
		}
	}
	if !slices.Contains(kinds, p.Kind) {
		return p, httpx.BadRequest("kind must be one of %s", strings.Join(kinds, ", "))
	}
	if p.Revises < 0 {
		return p, httpx.BadRequest("revises must be the id of a denied proposal")
	}
	return p, nil
}

// ErrCap is returned when the day's proposals are used up.
var ErrCap = httpx.Conflict("%d changes were already proposed in the last 24 hours, the most allowed", dailyCap)

// Propose stores a proposal and asks the owner to approve it.
func (s *Service) Propose(ctx context.Context, p Proposal) (ChangeRequest, approvals.Approval, error) {
	p, err := p.normalize()
	if err != nil {
		return ChangeRequest{}, approvals.Approval{}, err
	}
	left, err := s.left(ctx)
	if err != nil {
		return ChangeRequest{}, approvals.Approval{}, err
	}
	if left == 0 {
		return ChangeRequest{}, approvals.Approval{}, ErrCap
	}
	detail := p.What + "\n\nWhy: " + p.Why
	var revises any
	if p.Revises > 0 {
		old, err := s.Get(ctx, p.Revises)
		if err != nil {
			return ChangeRequest{}, approvals.Approval{}, err
		}
		if old.Status != StatusDenied && old.Status != StatusExpired {
			return ChangeRequest{}, approvals.Approval{}, httpx.BadRequest("change %d is %s: only a denied or expired proposal can be revised", p.Revises, old.Status)
		}
		revises = p.Revises
		detail = fmt.Sprintf("Revises #%d.\n\n%s", p.Revises, detail)
	}

	// The row, its approval and the link between them commit together, and the
	// approval is announced only after: the owner never sees a card without a
	// change behind it, nor a change its card can't settle.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeRequest{}, approvals.Approval{}, fmt.Errorf("store change request: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	now := stamp(s.now())
	res, err := tx.ExecContext(ctx, `INSERT INTO change_requests (title, what, why, kind, revises, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, p.Title, p.What, p.Why, p.Kind, revises, now, now)
	if err != nil {
		return ChangeRequest{}, approvals.Approval{}, fmt.Errorf("store change request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ChangeRequest{}, approvals.Approval{}, fmt.Errorf("store change request: %w", err)
	}
	payload, _ := json.Marshal(queuePayload{ID: id}) // plain struct: cannot fail
	a, announce, err := s.appr.Prepare(ctx, tx, approvals.Request{
		Kind:    KindApproval,
		Title:   "Build: " + p.Title + "?",
		Detail:  detail,
		Payload: payload,
		TTL:     proposalTTL,
	})
	if err != nil {
		return ChangeRequest{}, approvals.Approval{}, fmt.Errorf("ask to approve change request: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET approval_id = ? WHERE id = ?`, a.ID, id); err != nil {
		return ChangeRequest{}, approvals.Approval{}, fmt.Errorf("link change request to its approval: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ChangeRequest{}, approvals.Approval{}, fmt.Errorf("store change request: %w", err)
	}
	announce()
	s.events.Invalidate(proto.DomainChanges)
	c, err := s.Get(context.WithoutCancel(ctx), id)
	return c, a, err
}

// left is how many proposals the rolling 24 hours still allow.
func (s *Service) left(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM change_requests WHERE created_at > ?`,
		stamp(s.now().Add(-capWindow))).Scan(&n); err != nil {
		return 0, fmt.Errorf("count change requests: %w", err)
	}
	return max(dailyCap-n, 0), nil
}

type queuePayload struct {
	ID int64 `json:"id"`
}

// queue is the approval's executor: allowing a proposal queues it for the worker.
func (s *Service) queue(ctx context.Context, raw json.RawMessage) (string, error) {
	var p queuePayload
	if err := json.Unmarshal(raw, &p); err != nil || p.ID <= 0 {
		return "", errors.New("the approval does not name a change request")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE change_requests SET state = 'queued', updated_at = ? WHERE id = ? AND state = 'proposed'`,
		stamp(s.now()), p.ID)
	if err != nil {
		return "", fmt.Errorf("queue change request %d: %w", p.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("change request %d is gone or already queued", p.ID)
	}
	s.events.Invalidate(proto.DomainChanges)
	return "Queued for Claude Code to build", nil
}

// Claim hands a queued change to the worker.
func (s *Service) Claim(ctx context.Context, id int64) (ChangeRequest, error) {
	now := stamp(s.now())
	return s.transition(ctx, id, StatusQueued, `UPDATE change_requests SET state = 'working', stage = 'Starting', claimed_at = ?, updated_at = ?
		WHERE id = ? AND state = 'queued'`, now, now, id)
}

// Progress records the worker's latest step and any links it has made.
func (s *Service) Progress(ctx context.Context, id int64, stage, issueURL, prURL string) (ChangeRequest, error) {
	stage = strings.Join(strings.Fields(stage), " ")
	if stage == "" {
		return ChangeRequest{}, httpx.BadRequest("stage is required")
	}
	if utf8.RuneCountInString(stage) > maxStage {
		return ChangeRequest{}, httpx.BadRequest("stage can be at most %d characters", maxStage)
	}
	if err := checkURLs(issueURL, prURL); err != nil {
		return ChangeRequest{}, err
	}
	return s.transition(ctx, id, StatusWorking, `UPDATE change_requests SET stage = ?, issue_url = COALESCE(?, issue_url),
		pr_url = COALESCE(?, pr_url), updated_at = ? WHERE id = ? AND state = 'working'`,
		stage, nullable(issueURL), nullable(prURL), stamp(s.now()), id)
}

// Outcomes a worker can finish with.
var outcomes = []string{StatusMerged, StatusRejected, StatusFailed}

// Finish records how a change ended and tells the owner.
func (s *Service) Finish(ctx context.Context, id int64, outcome, result, issueURL, prURL string) (ChangeRequest, error) {
	if !slices.Contains(outcomes, outcome) {
		return ChangeRequest{}, httpx.BadRequest("outcome must be one of %s", strings.Join(outcomes, ", "))
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return ChangeRequest{}, httpx.BadRequest("result is required: what happened, in a line or two")
	}
	if utf8.RuneCountInString(result) > maxResult {
		return ChangeRequest{}, httpx.BadRequest("result can be at most %d characters", maxResult)
	}
	if err := checkURLs(issueURL, prURL); err != nil {
		return ChangeRequest{}, err
	}
	now := stamp(s.now())
	c, err := s.transition(ctx, id, StatusWorking, `UPDATE change_requests SET state = ?, stage = '', result = ?,
		issue_url = COALESCE(?, issue_url), pr_url = COALESCE(?, pr_url), finished_at = ?, updated_at = ?
		WHERE id = ? AND state = 'working'`, outcome, result, nullable(issueURL), nullable(prURL), now, now, id)
	if err != nil {
		return c, err
	}
	s.notify(context.WithoutCancel(ctx), c)
	return c, nil
}

// transition runs a guarded update and returns the change, or a 409 naming
// the status it needed when the change was not in it.
func (s *Service) transition(ctx context.Context, id int64, from, q string, args ...any) (ChangeRequest, error) {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ChangeRequest{}, fmt.Errorf("update change request %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c, err := s.Get(ctx, id)
		if err != nil {
			return c, err
		}
		return c, httpx.Conflict("change request %d is %s, not %s", id, c.Status, from)
	}
	s.events.Invalidate(proto.DomainChanges)
	return s.Get(ctx, id)
}

func checkURLs(urls ...string) error {
	for _, u := range urls {
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, urlPrefix) || len(u) > maxURL || strings.ContainsAny(u, " \t\r\n\"'<>") {
			return httpx.BadRequest("links must be %s… URLs", urlPrefix)
		}
	}
	return nil
}

var finishTitles = map[string]string{
	StatusMerged:   "Change merged",
	StatusRejected: "Change rejected",
	StatusFailed:   "Change failed",
}

// notify tells the owner by notify event and push how a change ended.
func (s *Service) notify(ctx context.Context, c ChangeRequest) {
	title, body := finishTitles[c.Status], c.Title
	if c.Result != nil {
		body += ": " + *c.Result
	}
	s.events.Notify(proto.NewNotify("change-"+c.ID, proto.NotifyKindChange, title, body, nil))
	if s.pusher == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, deliverTimout)
	defer cancel()
	if _, err := s.pusher.Send(pctx, push.Payload{Title: title, Body: body, URL: "/?menu=changes", Tag: "change-" + c.ID}, push.UrgencyNormal); err != nil {
		s.log.Error("cannot send push for a change request", "id", c.ID, "error", err)
	}
}
