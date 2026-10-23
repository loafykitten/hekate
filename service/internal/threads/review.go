package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// maybeReview starts a review of a finished turn's work when the turn changed the lab and the
// round allows another, and says whether it did. It runs inside the agent's end of turn, so it
// only reads the database and starts the review's goroutine; the thread counts as working from
// here, so nothing can slip in between the turn and its review.
func (c *conv) maybeReview(m proto.TurnEnd, info turnInfo) bool {
	s := c.s
	rv := s.opts.Reviewer
	if m.Status != proto.StatusDone || rv == nil || !rv.Ready() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 10*time.Second)
	defer cancel()
	actions, changed, err := s.turnActions(ctx, m.TurnID)
	if err != nil {
		s.log.Warn("threads: can't read a turn's work to review it", "thread", c.id, "error", err)
		return false
	}
	if !changed {
		return false
	}
	round := 1
	var prev *review.Report
	if info.origin == proto.OriginReview {
		v, ok, err := rv.ForFollowup(ctx, m.TurnID)
		if err != nil || !ok {
			return false
		}
		if round = v.Round + 1; round > review.MaxRounds {
			return false
		}
		rep, err := rv.Report(ctx, v.ID)
		if err != nil {
			return false
		}
		prev = &rep
	}
	v, err := rv.Begin(ctx, c.id, m.TurnID, round)
	if err != nil {
		s.log.Warn("threads: can't start a review", "thread", c.id, "error", err)
		return false
	}
	rctx, rcancel := context.WithCancelCause(s.baseCtx)
	c.mu.Lock()
	c.review = &reviewing{cancel: rcancel}
	c.mu.Unlock()
	s.hub.Broadcast(proto.NewThreadEvent(c.id, proto.NewReview(v)))
	s.events.Invalidate("threads")
	s.reviewsWG.Add(1)
	go c.runReview(rctx, rcancel, v, actions, prev)
	return true
}

// runReview runs the review, records it, and hands her the findings in a new turn.
func (c *conv) runReview(ctx context.Context, cancel context.CancelCauseFunc, v proto.Review, actions []string, prev *review.Report) {
	s := c.s
	rv := s.opts.Reviewer
	defer s.reviewsWG.Done()
	defer cancel(nil)
	req := review.Request{Round: v.Round, Actions: actions, Previous: prev}
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

// turnActions lists what a turn did with its tools, for the reviewer, and whether it changed the
// lab: wrote a file there, or ran a command that isn't read-only.
func (s *Service) turnActions(ctx context.Context, turnID string) ([]string, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool_calls FROM messages WHERE turn_id = ? AND role = 'assistant'
		AND tool_calls != '' ORDER BY id`, turnID)
	if err != nil {
		return nil, false, fmt.Errorf("turn actions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var (
		actions []string
		changed bool
	)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, false, fmt.Errorf("turn actions: %w", err)
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
					changed = true
				}
			case "lab_write_file":
				actions = append(actions, "lab_write_file: wrote "+str("path"))
				changed = true
			case "web_search":
				actions = append(actions, "web_search: "+str("query"))
			case "web_read":
				actions = append(actions, "web_read: "+str("url"))
			default:
				actions = append(actions, c.Name+" "+clip(c.Arguments, 200))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("turn actions: %w", err)
	}
	return actions, changed, nil
}

// conversationText is what the owner wrote in the thread and her replies, a turn's reply as one,
// oldest first.
func (s *Service) conversationText(ctx context.Context, id string) (owner, replies []string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT turn_id, role, text FROM messages WHERE conversation_id = ?
		AND role IN ('user', 'assistant') AND text != '' ORDER BY id`, id)
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
