package review

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/proto"
)

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Begin records a review starting on a thread turn's work.
func (r *Reviewer) Begin(ctx context.Context, threadID, turnID string, round int) (proto.Review, error) {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail on supported platforms
	id := "rv_" + hex.EncodeToString(b[:])
	if _, err := r.db.ExecContext(ctx, `INSERT INTO reviews (id, thread_id, turn_id, round, status, model, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, threadID, turnID, round, proto.ReviewRunning, r.model.Name, stamp(r.now())); err != nil {
		return proto.Review{}, fmt.Errorf("record review: %w", err)
	}
	return r.Get(ctx, id)
}

// Finish records how a review ended: its report when done, or why it failed or stopped.
func (r *Reviewer) Finish(ctx context.Context, id, status string, rep Report, calls []Call, failure string) (proto.Review, error) {
	if calls == nil {
		calls = []Call{}
	}
	findings, verified := rep.Findings, rep.Verified
	if findings == nil {
		findings = []Finding{}
	}
	if verified == nil {
		verified = []string{}
	}
	fj, _ := json.Marshal(findings)
	vj, _ := json.Marshal(verified)
	cj, _ := json.Marshal(calls)
	if _, err := r.db.ExecContext(ctx, `UPDATE reviews SET status = ?, verdict = ?, summary = ?, findings = ?, verified = ?,
		calls = ?, error = ?, finished_at = ? WHERE id = ?`, status, rep.Verdict, rep.Summary, string(fj), string(vj),
		string(cj), failure, stamp(r.now()), id); err != nil {
		return proto.Review{}, fmt.Errorf("finish review: %w", err)
	}
	return r.Get(ctx, id)
}

// SetFollowup records the turn that handed her a review's findings.
func (r *Reviewer) SetFollowup(ctx context.Context, id, turnID string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE reviews SET followup_turn_id = ? WHERE id = ?`, turnID, id); err != nil {
		return fmt.Errorf("record the review's turn: %w", err)
	}
	return nil
}

// AbortRunning marks reviews left running by the last shutdown as failed.
func (r *Reviewer) AbortRunning(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE reviews SET status = ?, error = ?, finished_at = ? WHERE status = ?`,
		proto.ReviewFailed, "Hekate restarted while the review ran.", stamp(r.now()), proto.ReviewRunning); err != nil {
		return fmt.Errorf("abort running reviews: %w", err)
	}
	return nil
}

const columns = `id, thread_id, turn_id, round, status, model, verdict, summary, findings, verified, calls, error,
	followup_turn_id, started_at, COALESCE(finished_at, '')`

func scan(row interface{ Scan(...any) error }) (proto.Review, Report, error) {
	var (
		v                proto.Review
		fj, vj, cj       string
		started, finshed string
	)
	if err := row.Scan(&v.ID, &v.ThreadID, &v.TurnID, &v.Round, &v.Status, &v.Model, &v.Verdict, &v.Summary, &fj, &vj, &cj,
		&v.Error, &v.FollowupTurnID, &started, &finshed); err != nil {
		return proto.Review{}, Report{}, err //nolint:wrapcheck // callers tell sql.ErrNoRows apart
	}
	var rep Report
	_ = json.Unmarshal([]byte(fj), &rep.Findings)
	_ = json.Unmarshal([]byte(vj), &rep.Verified)
	var calls []Call
	_ = json.Unmarshal([]byte(cj), &calls)
	rep.Verdict, rep.Summary = v.Verdict, v.Summary
	v.Findings = make([]proto.ReviewFinding, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		v.Findings = append(v.Findings, proto.ReviewFinding(f))
	}
	v.Verified = rep.Verified
	if v.Verified == nil {
		v.Verified = []string{}
	}
	v.Commands = len(calls)
	v.StartedAt, v.FinishedAt = started, finshed
	return v, rep, nil
}

// Get returns one review.
func (r *Reviewer) Get(ctx context.Context, id string) (proto.Review, error) {
	v, _, err := scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM reviews WHERE id = ?`, id))
	if err != nil {
		return proto.Review{}, fmt.Errorf("get review %s: %w", id, err)
	}
	return v, nil
}

// Report returns a finished review's report, for the next round.
func (r *Reviewer) Report(ctx context.Context, id string) (Report, error) {
	_, rep, err := scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM reviews WHERE id = ?`, id))
	if err != nil {
		return Report{}, fmt.Errorf("get review %s: %w", id, err)
	}
	return rep, nil
}

// ForFollowup returns the review whose findings started a turn, if any.
func (r *Reviewer) ForFollowup(ctx context.Context, turnID string) (proto.Review, bool, error) {
	v, _, err := scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM reviews WHERE followup_turn_id = ? AND followup_turn_id != ''`, turnID))
	if errors.Is(err, sql.ErrNoRows) {
		return proto.Review{}, false, nil
	}
	if err != nil {
		return proto.Review{}, false, fmt.Errorf("review for turn %s: %w", turnID, err)
	}
	return v, true, nil
}

// ForTurns returns a thread's reviews of the given turns, or that started them, oldest first.
func (r *Reviewer) ForTurns(ctx context.Context, threadID string, turnIDs []string) ([]proto.Review, error) {
	out := []proto.Review{}
	if len(turnIDs) == 0 {
		return out, nil
	}
	marks := "?" + strings.Repeat(", ?", len(turnIDs)-1)
	args := []any{threadID}
	for range 2 {
		for _, t := range turnIDs {
			args = append(args, t)
		}
	}
	//nolint:gosec // only placeholders are joined; every value is bound
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM reviews WHERE thread_id = ?
		AND (turn_id IN (`+marks+`) OR followup_turn_id IN (`+marks+`)) ORDER BY started_at, rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("thread reviews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		v, _, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("thread reviews: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("thread reviews: %w", err)
	}
	return out, nil
}

// FollowupNote is what she reads as the start of the turn that hands her a review's findings.
func FollowupNote(v proto.Review, tamer string) string {
	if tamer == "" {
		tamer = "The owner"
	}
	var b strings.Builder
	last := ""
	if v.Round >= MaxRounds {
		last = " This was the last round: nothing reviews your fixes this time, so check them yourself."
	}
	fmt.Fprintf(&b, "(Not from %s: a reviewer, a second AI model from another family than yours, checked the lab work you just "+
		"did, against the lab itself. %s sees this review on screen as written.%s)\n\n", tamer, tamer, last)
	b.WriteString("Summary: " + v.Summary + "\n\nFindings:\n")
	for i, f := range v.Findings {
		fmt.Fprintf(&b, "\n%d. [%s] %s\n", i+1, f.Severity, f.Title)
		if f.Claim != "" {
			b.WriteString("   What you said or assumed: " + f.Claim + "\n")
		}
		if f.Evidence != "" {
			b.WriteString("   Evidence: " + f.Evidence + "\n")
		}
		if f.Unverified {
			b.WriteString("   (The reviewer couldn't check this one in the lab: it rests on what it knows.)\n")
		}
		if f.Fix != "" {
			b.WriteString("   Suggested fix: " + f.Fix + "\n")
		}
	}
	b.WriteString("\nWork through each finding: fix it in the lab, or say why it doesn't apply and show the evidence " +
		"(reviewers are wrong sometimes, and a suggested fix can be wrong even when the problem is real). " +
		"Then tell " + tamer + " briefly what you changed and what you didn't, and why.")
	return b.String()
}
