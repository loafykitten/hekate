package threads

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/lab"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/review"
)

// Why a review was cancelled.
var (
	errStopped  = errors.New("stopped by the owner")
	errShutdown = errors.New("the service is shutting down")
)

// reviewing is a review of a turn's work in progress; the thread counts as working meanwhile.
type reviewing struct {
	cancel context.CancelCauseFunc
}

// Reviewing reports whether a review of the thread's work is in progress.
func (c *conv) reviewing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.review != nil
}

// stopReview cancels a review in progress, as the owner's stop.
func (c *conv) stopReview() {
	c.mu.Lock()
	rv := c.review
	c.mu.Unlock()
	if rv != nil {
		rv.cancel(errStopped)
	}
}

// storedCall is a tool call as the agent stores it on an assistant message.
type storedCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// maybeReview starts a review when a turn ends, and says whether it did. A review covers a piece
// of finished work: everything she changed in the lab since the last review. It starts when she
// hands work over (finish_work) or when she's answered a review's findings and the rounds allow
// another; after a step along the way the thread waits to go quiet instead. It runs inside the
// agent's end of turn, so it only reads the database and starts the review's goroutine; the thread
// counts as working from here, so nothing can slip in between the turn and its review.
func (c *conv) maybeReview(m proto.TurnEnd, info turnInfo) bool {
	s := c.s
	rv := s.opts.Reviewer
	if rv == nil || !rv.Ready() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 10*time.Second)
	defer cancel()
	w, err := s.pending(ctx, c.id)
	if err != nil {
		s.log.Warn("threads: can't read her work to review it", "thread", c.id, "error", err)
		return false
	}
	if w.changes == 0 {
		return false
	}
	if m.Status != proto.StatusDone {
		// Stopped or failed: what she changed is still in the lab, unreviewed.
		if w.own > 0 {
			c.armQuiet()
		}
		return false
	}
	round := 1
	var prev *review.Report
	switch {
	case info.origin == proto.OriginReview:
		v, ok, err := rv.ForFollowup(ctx, m.TurnID)
		if err != nil || !ok {
			return false
		}
		if round = v.Round + 1; round > review.MaxRounds {
			return false // the end of the chain: these fixes go with her next piece of work
		}
		rep, err := rv.Report(ctx, v.ID)
		if err != nil {
			return false
		}
		prev = &rep
	case w.finishedIn != m.TurnID:
		if w.own > 0 {
			c.armQuiet()
		}
		return false
	}
	return c.startReview(ctx, m.TurnID, round, w, prev)
}

// startReview records a review of w as running and starts it.
func (c *conv) startReview(ctx context.Context, turnID string, round int, w work, prev *review.Report) bool {
	s := c.s
	v, err := s.opts.Reviewer.Begin(ctx, c.id, turnID, round)
	if err != nil {
		s.log.Warn("threads: can't start a review", "thread", c.id, "error", err)
		return false
	}
	c.stopQuiet()
	rctx, rcancel := context.WithCancelCause(s.baseCtx)
	c.mu.Lock()
	c.review = &reviewing{cancel: rcancel}
	c.mu.Unlock()
	s.hub.Broadcast(proto.NewThreadEvent(c.id, proto.NewReview(v)))
	s.events.Invalidate("threads")
	s.reviewsWG.Add(1)
	go c.runReview(rctx, rcancel, v, w, prev)
	return true
}

// armQuiet (re)starts the wait after a step along the way: if the thread stays quiet that long
// with lab changes unreviewed, they're reviewed then.
func (c *conv) armQuiet() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quiet != nil {
		c.quiet.Stop()
	}
	c.quiet = time.AfterFunc(c.s.quietAfter, c.reviewIfQuiet)
}

func (c *conv) stopQuiet() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quiet != nil {
		c.quiet.Stop()
		c.quiet = nil
	}
}

// armPending starts the quiet wait for threads whose lab changes went unreviewed when the service
// last stopped: the timers don't outlive it.
func (s *Service) armPending(ctx context.Context) error {
	if rv := s.opts.Reviewer; rv == nil || !rv.Ready() {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM threads WHERE archived = 0`)
	if err != nil {
		return fmt.Errorf("pending reviews: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("pending reviews: %w", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pending reviews: %w", err)
	}
	for _, id := range ids {
		w, err := s.pending(ctx, id)
		if err != nil {
			return err
		}
		if w.own == 0 {
			continue
		}
		c, err := s.conv(ctx, id)
		if err != nil {
			return err
		}
		s.log.Info("threads: lab changes await review", "thread", id, "changes", w.changes)
		c.armQuiet()
	}
	return nil
}

// reviewIfQuiet reviews her unreviewed lab changes once the thread has gone quiet. It holds the
// service's lock, as say does, so new input can't start between the check and the review.
func (c *conv) reviewIfQuiet() {
	s := c.s
	rv := s.opts.Reviewer
	if s.baseCtx.Err() != nil || rv == nil || !rv.Ready() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ag.Busy() || c.reviewing() {
		return // that turn's end, or the review's, decides what comes next
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 10*time.Second)
	defer cancel()
	var archived bool
	if err := s.db.QueryRowContext(ctx, `SELECT archived FROM threads WHERE id = ?`, c.id).Scan(&archived); err != nil || archived {
		return
	}
	running := 0
	for _, other := range s.convs {
		if other.ag.Busy() || other.reviewing() {
			running++
		}
	}
	if running >= maxRunning {
		c.mu.Lock()
		c.quiet = time.AfterFunc(s.quietAfter, c.reviewIfQuiet) // try again later
		c.mu.Unlock()
		return
	}
	w, err := s.pending(ctx, c.id)
	if err != nil {
		s.log.Warn("threads: can't read her work to review it", "thread", c.id, "error", err)
		return
	}
	if w.own == 0 {
		return
	}
	s.log.Info("threads: reviewing work left unreviewed", "thread", c.id, "changes", w.changes)
	c.startReview(ctx, w.last, 1, w, nil)
}

// runReview runs the review, records it, and hands her the findings in a new turn.
func (c *conv) runReview(ctx context.Context, cancel context.CancelCauseFunc, v proto.Review, w work, prev *review.Report) {
	s := c.s
	rv := s.opts.Reviewer
	defer s.reviewsWG.Done()
	defer cancel(nil)
	req := review.Request{Round: v.Round, Actions: w.actions, Handover: w.handover, Previous: prev, Budget: review.Budget(w.changes)}
	if prev == nil {
		req.Earlier = s.earlierReview(ctx, c.id, v.ID)
	}
	var err error
	req.Owner, req.Replies, err = s.conversationText(ctx, c.id)
	var (
		rep   review.Report
		calls []review.Call
	)
	if err == nil {
		s.log.Info("threads: reviewing", "thread", c.id, "turn", v.TurnID, "round", v.Round)
		rep, calls, err = rv.Run(ctx, req)
	}
	status, failure := proto.ReviewDone, ""
	switch {
	case errors.Is(context.Cause(ctx), errStopped):
		status = proto.ReviewStopped
	case ctx.Err() != nil:
		status, failure = proto.ReviewFailed, "Hekate restarted while the review ran."
	case err != nil:
		status, failure = proto.ReviewFailed, clip(err.Error(), 300)
		s.log.Warn("threads: review failed", "thread", c.id, "error", err)
	}
	dbctx, dbcancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 10*time.Second)
	defer dbcancel()
	done, ferr := rv.Finish(dbctx, v.ID, status, rep, calls, failure)
	if ferr != nil {
		s.log.Error("threads: can't record a review", "thread", c.id, "error", ferr)
		done = v
		done.Status, done.Error = proto.ReviewFailed, "The review couldn't be saved."
	}
	s.log.Info("threads: review ended", "thread", c.id, "status", done.Status, "verdict", done.Verdict,
		"findings", len(done.Findings), "commands", done.Commands)

	// Handing over to her next turn happens under the service's lock, as say does, so new input
	// can't start between the review and the turn that answers it.
	s.mu.Lock()
	c.mu.Lock()
	c.review = nil
	c.mu.Unlock()
	followup := ""
	if done.Status == proto.ReviewDone && len(done.Findings) > 0 {
		followup = c.ag.Prompt(proto.OriginReview, newID(), review.FollowupNote(done, s.opts.Tamer))
	}
	s.mu.Unlock()
	if followup != "" {
		if err := rv.SetFollowup(dbctx, done.ID, followup); err != nil {
			s.log.Error("threads: can't link a review to its turn", "thread", c.id, "error", err)
		}
		done.FollowupTurnID = followup
	}
	s.hub.Broadcast(proto.NewThreadEvent(c.id, proto.NewReview(done)))
	s.touched(c.id, nil)
	if followup == "" && done.Status != proto.ReviewStopped {
		s.reviewed(dbctx, c.id, done)
	}
}

// reviewed tells the owner a review ended with nothing for her to do: it's the end of the work.
func (s *Service) reviewed(ctx context.Context, id string, v proto.Review) {
	var title string
	if err := s.db.QueryRowContext(ctx, `SELECT title FROM threads WHERE id = ?`, id).Scan(&title); err != nil {
		s.log.Warn("threads: can't read a thread to notify about it", "thread", id, "error", err)
		return
	}
	body := "Second opinion: no problems found. " + v.Summary
	if v.Status == proto.ReviewFailed {
		body = "Her work is done, but the second opinion couldn't be had: " + v.Error
	}
	body = clip(body, notifyBody)
	s.events.Notify(proto.NewNotify(id, "thread", title, body, nil))
	if s.pusher != nil {
		if _, err := s.pusher.Send(ctx, push.Payload{Title: title, Body: body, URL: "/?thread=" + id, Tag: "thread-" + id}, push.UrgencyNormal); err != nil {
			s.log.Warn("threads: push failed", "thread", id, "error", err)
		}
	}
}

// work is what she did in the lab since its last review.
type work struct {
	actions []string // her tool calls, oldest first, each turn's under a line saying which
	changes int      // how many of them changed the lab
	// own counts changes from the owner's turns, not from her answers to a review: only those wait
	// for a quiet review, so a chain of reviews never restarts itself.
	own        int
	handover   string // what she handed over as done (finish_work), the latest
	finishedIn string // the turn she last called finish_work in
	last       string // the latest turn
}

// pending is her work since the thread's last review: the turns after the one it reviewed.
func (s *Service) pending(ctx context.Context, thread string) (work, error) {
	var after int64
	err := s.db.QueryRowContext(ctx, `SELECT t.rowid FROM reviews r JOIN turns t ON t.id = r.turn_id
		WHERE r.thread_id = ? ORDER BY r.rowid DESC LIMIT 1`, thread).Scan(&after)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return work{}, fmt.Errorf("pending work: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, origin, user_text FROM turns WHERE conversation_id = ? AND rowid > ?
		ORDER BY rowid`, thread, after)
	if err != nil {
		return work{}, fmt.Errorf("pending work: %w", err)
	}
	type turn struct{ id, origin, said string }
	var turns []turn
	for rows.Next() {
		var t turn
		if err := rows.Scan(&t.id, &t.origin, &t.said); err != nil {
			_ = rows.Close()
			return work{}, fmt.Errorf("pending work: %w", err)
		}
		turns = append(turns, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return work{}, fmt.Errorf("pending work: %w", err)
	}
	var w work
	for _, t := range turns {
		w.last = t.id
		actions, changes, handover, err := s.turnActions(ctx, t.id)
		if err != nil {
			return work{}, err
		}
		if handover != "" {
			w.handover, w.finishedIn = handover, t.id
		}
		if len(actions) == 0 {
			continue
		}
		head := fmt.Sprintf("(her turn after the owner said %q)", clip(t.said, 100))
		if t.origin == proto.OriginReview {
			head = "(her turn answering a review's findings)"
		}
		w.actions = append(append(w.actions, head), actions...)
		w.changes += changes
		if t.origin != proto.OriginReview {
			w.own += changes
		}
	}
	return w, nil
}

// earlierReview is the thread's last finished review before this one, for the bigger picture.
func (s *Service) earlierReview(ctx context.Context, thread, except string) *review.Report {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM reviews WHERE thread_id = ? AND id != ? AND status = ?
		ORDER BY rowid DESC LIMIT 1`, thread, except, proto.ReviewDone).Scan(&id); err != nil {
		return nil
	}
	rep, err := s.opts.Reviewer.Report(ctx, id)
	if err != nil {
		return nil
	}
	return &rep
}

// turnActions lists what a turn did with its tools, for the reviewer: how many of its actions
// changed the lab (wrote a file there, or ran a command that isn't read-only), and what it handed
// over as done, if it called finish_work.
func (s *Service) turnActions(ctx context.Context, turnID string) (actions []string, changes int, handover string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool_calls FROM messages WHERE turn_id = ? AND role = 'assistant'
		AND tool_calls != '' ORDER BY id`, turnID)
	if err != nil {
		return nil, 0, "", fmt.Errorf("turn actions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, "", fmt.Errorf("turn actions: %w", err)
		}
		var calls []storedCall
		if json.Unmarshal([]byte(raw), &calls) != nil {
			continue
		}
		for _, c := range calls {
			var a map[string]any
			_ = json.Unmarshal([]byte(c.Arguments), &a)
			str := func(k string) string { v, _ := a[k].(string); return v }
			switch c.Name {
			case "lab_shell":
				cmd := str("command")
				actions = append(actions, "lab_shell: "+cmd)
				if lab.ReadOnly(cmd) != nil {
					changes++
				}
			case "lab_write_file":
				actions = append(actions, "lab_write_file: wrote "+str("path"))
				changes++
			case "web_search":
				actions = append(actions, "web_search: "+str("query"))
			case "web_read":
				actions = append(actions, "web_read: "+str("url"))
			case finishName:
				handover = cmp.Or(strings.TrimSpace(str("summary")), "(no summary)")
			default:
				actions = append(actions, c.Name+" "+clip(c.Arguments, 200))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", fmt.Errorf("turn actions: %w", err)
	}
	return actions, changes, handover, nil
}

const finishName = "finish_work"

// finishTool is how she hands over a piece of finished lab work, which starts its review.
func (s *Service) finishTool() agent.Tool {
	return agent.Tool{
		Name: finishName,
		Description: "Call this in the turn where you hand the owner finished lab work: what they asked for is set up or " +
			"changed, and you're telling them it's done and how to use it. When the turn ends, a second model reviews " +
			"everything you've changed in the lab since its last review. Don't call it for a step along the way or when " +
			"you're asking them something: lab changes left without it are reviewed once the thread has been quiet a while.",
		Schema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{
			"summary":{"type":"string","description":"What's done and what you're telling the owner works: services, addresses, logins and where they're kept, what you checked."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Handing over the work",
		Run: func(context.Context, json.RawMessage) (agent.Result, error) {
			return agent.Result{Content: "Noted. When this turn ends, your lab work since the last review goes to the reviewer; " +
				"finish telling the owner what's done."}, nil
		},
	}
}

// conversationText is what the owner wrote in the thread and her replies, a turn's reply as one,
// oldest first.
func (s *Service) conversationText(ctx context.Context, id string) (owner, replies []string, err error) {
	// The owner's side as she read it: their words, and where any files they sent are.
	rows, err := s.db.QueryContext(ctx, `SELECT turn_id, role, CASE role WHEN 'user' THEN content ELSE text END
		FROM messages WHERE conversation_id = ? AND role IN ('user', 'assistant') AND text != '' ORDER BY id`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("thread text: %w", err)
	}
	defer func() { _ = rows.Close() }()
	last := ""
	for rows.Next() {
		var turn, role, text string
		if err := rows.Scan(&turn, &role, &text); err != nil {
			return nil, nil, fmt.Errorf("thread text: %w", err)
		}
		if role == "user" {
			owner = append(owner, text)
			continue
		}
		if turn == last && len(replies) > 0 {
			replies[len(replies)-1] += "\n\n" + text
		} else {
			replies = append(replies, text)
		}
		last = turn
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("thread text: %w", err)
	}
	return owner, replies, nil
}
