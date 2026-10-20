package inspect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Idea statuses and kinds.
const (
	IdeaOpen      = "open"
	IdeaDone      = "done"
	IdeaDismissed = "dismissed"
)

var (
	ideaStatuses = []string{IdeaOpen, IdeaDone, IdeaDismissed}
	ideaKinds    = []string{"tool", "fix", "improvement", "other"}
)

const (
	maxIdea       = 500
	ideasPerDay   = 10
	maxIdeaList   = 200
	ideaCapWindow = 24 * time.Hour
)

// ErrIdeaCap is returned once she has noted ideasPerDay ideas in a day.
var ErrIdeaCap = errors.New("that's 10 ideas in the last day")

// Idea is the API shape from PROTOCOL.md.
type Idea struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// LogIdea saves an idea. An open idea with the same text is returned instead
// of a copy (existed true).
func (s *Service) LogIdea(ctx context.Context, text, kind string) (Idea, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Idea{}, false, httpx.BadRequest("an idea needs some text")
	}
	if utf8.RuneCountInString(text) > maxIdea {
		return Idea{}, false, httpx.BadRequest("keep an idea under %d characters", maxIdea)
	}
	if kind == "" {
		kind = "other"
	}
	if !slices.Contains(ideaKinds, kind) {
		return Idea{}, false, httpx.BadRequest("kind must be one of %s", strings.Join(ideaKinds, ", "))
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM ideas WHERE status = ? AND lower(text) = lower(?)`, IdeaOpen, text).Scan(&id)
	switch {
	case err == nil:
		idea, err := s.idea(ctx, id)
		return idea, true, err
	case !errors.Is(err, sql.ErrNoRows):
		return Idea{}, false, fmt.Errorf("look for the idea: %w", err)
	}
	now := s.now()
	var recent int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ideas WHERE created_at >= ?`, stamp(now.Add(-ideaCapWindow))).Scan(&recent); err != nil {
		return Idea{}, false, fmt.Errorf("count ideas: %w", err)
	}
	if recent >= ideasPerDay {
		return Idea{}, false, ErrIdeaCap
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO ideas (text, kind, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		text, kind, IdeaOpen, stamp(now), stamp(now))
	if err != nil {
		return Idea{}, false, fmt.Errorf("save the idea: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return Idea{}, false, fmt.Errorf("save the idea: %w", err)
	}
	s.events.Invalidate(DomainIdeas)
	idea, err := s.idea(ctx, id)
	return idea, false, err
}

// Ideas lists ideas, newest first: open ones, or every status with all.
func (s *Service) Ideas(ctx context.Context, all bool) ([]Idea, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, kind, status, created_at, updated_at FROM ideas
		WHERE ? OR status = ? ORDER BY id DESC LIMIT ?`, all, IdeaOpen, maxIdeaList)
	if err != nil {
		return nil, fmt.Errorf("list ideas: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Idea{}
	for rows.Next() {
		idea, err := scanIdea(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, idea)
	}
	return out, rows.Err()
}

// SetIdeaStatus marks an idea done, dismissed or open again.
func (s *Service) SetIdeaStatus(ctx context.Context, id int64, status string) (Idea, error) {
	if !slices.Contains(ideaStatuses, status) {
		return Idea{}, httpx.BadRequest("status must be one of %s", strings.Join(ideaStatuses, ", "))
	}
	res, err := s.db.ExecContext(ctx, `UPDATE ideas SET status = ?, updated_at = ? WHERE id = ?`, status, stamp(s.now()), id)
	if err != nil {
		return Idea{}, fmt.Errorf("update the idea: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Idea{}, httpx.NotFound("no idea %d", id)
	}
	s.events.Invalidate(DomainIdeas)
	return s.idea(ctx, id)
}

// DeleteIdea removes an idea.
func (s *Service) DeleteIdea(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ideas WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete the idea: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.NotFound("no idea %d", id)
	}
	s.events.Invalidate(DomainIdeas)
	return nil
}

func (s *Service) idea(ctx context.Context, id int64) (Idea, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, text, kind, status, created_at, updated_at FROM ideas WHERE id = ?`, id)
	idea, err := scanIdea(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Idea{}, httpx.NotFound("no idea %d", id)
	}
	return idea, err
}

func scanIdea(row interface{ Scan(...any) error }) (Idea, error) {
	var idea Idea
	var id int64
	if err := row.Scan(&id, &idea.Text, &idea.Kind, &idea.Status, &idea.CreatedAt, &idea.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Idea{}, err
		}
		return Idea{}, fmt.Errorf("scan idea: %w", err)
	}
	idea.ID = fmt.Sprint(id)
	return idea, nil
}
