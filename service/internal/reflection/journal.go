package reflection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/memory"
)

const (
	defaultDays = 14
	maxDays     = 60
)

// DiaryEntry is one day's diary entry.
type DiaryEntry struct {
	Day       string `json:"day"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Change is one thing a reflection changed in her memories.
type Change struct {
	ID       string         `json:"id"`
	Action   string         `json:"action"` // save, update, retire
	MemoryID string         `json:"memory_id"`
	Before   *memory.Memory `json:"before"` // null for save
	After    *memory.Memory `json:"after"`  // null for retire
	Reason   string         `json:"reason"`
	UndoneAt *string        `json:"undone_at"`

	mergedFrom string // update: the ids merged in, comma-separated
}

// Reflection is one night's reflection and what it changed.
type Reflection struct {
	ID        string   `json:"id"`
	Summary   string   `json:"summary"`
	CreatedAt string   `json:"created_at"`
	Changes   []Change `json:"changes"`
}

// JournalDay is a day in the journal: her diary entry and the reflections on it.
type JournalDay struct {
	Day         string       `json:"day"`
	Diary       *DiaryEntry  `json:"diary"`
	Reflections []Reflection `json:"reflections"`
}

// Journal lists the last days with a diary entry or a reflection, newest first.
func (s *Service) Journal(ctx context.Context, limit int) ([]JournalDay, error) {
	if limit <= 0 {
		limit = defaultDays
	}
	limit = min(limit, maxDays)
	rows, err := s.db.QueryContext(ctx, `SELECT day FROM (SELECT day FROM diary UNION SELECT day FROM reflections)
		ORDER BY day DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list journal days: %w", err)
	}
	var days []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list journal days: %w", err)
		}
		days = append(days, d)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list journal days: %w", err)
	}
	out := make([]JournalDay, 0, len(days))
	for _, d := range days {
		jd := JournalDay{Day: d, Reflections: []Reflection{}}
		if jd.Diary, err = s.diary(ctx, d); err != nil {
			return nil, err
		}
		if jd.Reflections, err = s.reflections(ctx, d); err != nil {
			return nil, err
		}
		out = append(out, jd)
	}
	return out, nil
}

// diary returns a day's entry, or nil.
func (s *Service) diary(ctx context.Context, day string) (*DiaryEntry, error) {
	var e DiaryEntry
	err := s.db.QueryRowContext(ctx, `SELECT day, text, created_at, updated_at FROM diary WHERE day = ?`, day).
		Scan(&e.Day, &e.Text, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no entry that day
	}
	if err != nil {
		return nil, fmt.Errorf("read the diary: %w", err)
	}
	return &e, nil
}

// latestDiary returns the newest entry before day (any day when day is empty), or nil.
func (s *Service) latestDiary(ctx context.Context, before string) (*DiaryEntry, error) {
	var day string
	err := s.db.QueryRowContext(ctx, `SELECT day FROM diary WHERE ? = '' OR day < ? ORDER BY day DESC LIMIT 1`, before, before).Scan(&day)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no entry yet
	}
	if err != nil {
		return nil, fmt.Errorf("read the diary: %w", err)
	}
	return s.diary(ctx, day)
}

func (s *Service) reflections(ctx context.Context, day string) ([]Reflection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, summary, created_at FROM reflections WHERE day = ? ORDER BY id DESC`, day)
	if err != nil {
		return nil, fmt.Errorf("list reflections: %w", err)
	}
	var out []Reflection
	for rows.Next() {
		var r Reflection
		var id int64
		if err := rows.Scan(&id, &r.Summary, &r.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list reflections: %w", err)
		}
		r.ID = fmt.Sprint(id)
		out = append(out, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reflections: %w", err)
	}
	for i := range out {
		if out[i].Changes, err = s.changes(ctx, changesOfReflection, out[i].ID); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []Reflection{}
	}
	return out, nil
}

// The two ways changes are looked up.
const (
	changesOfReflection = `SELECT id, action, memory_id, before, after, reason, undone_at, merged_from FROM reflection_changes
		WHERE reflection_id = ? ORDER BY id`
	changeByID = `SELECT id, action, memory_id, before, after, reason, undone_at, merged_from FROM reflection_changes WHERE id = ?`
)

func (s *Service) changes(ctx context.Context, query string, arg any) ([]Change, error) {
	rows, err := s.db.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("list reflection changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Change{}
	for rows.Next() {
		var c Change
		var id, mid int64
		var before, after string
		var undone sql.NullString
		if err := rows.Scan(&id, &c.Action, &mid, &before, &after, &c.Reason, &undone, &c.mergedFrom); err != nil {
			return nil, fmt.Errorf("list reflection changes: %w", err)
		}
		c.ID, c.MemoryID = fmt.Sprint(id), fmt.Sprint(mid)
		c.Before, c.After = snapshot(before), snapshot(after)
		if undone.Valid {
			c.UndoneAt = &undone.String
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func snapshot(raw string) *memory.Memory {
	if raw == "" {
		return nil
	}
	var m memory.Memory
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return &m
}

// Undo reverses one change: a saved memory is retired, an updated one goes
// back to how it was, a retired one comes back. Each change is undone once.
func (s *Service) Undo(ctx context.Context, changeID int64) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.changes(ctx, changeByID, changeID)
	if err != nil {
		return Change{}, err
	}
	if len(list) == 0 {
		return Change{}, httpx.NotFound("no reflection change %d", changeID)
	}
	c := list[0]
	if c.UndoneAt != nil {
		return Change{}, httpx.Conflict("that change was already undone")
	}
	var mid int64
	_, _ = fmt.Sscan(c.MemoryID, &mid)
	now := stamp(s.now())
	// Marking it undone happens in the memory change's own transaction.
	mark := func(ctx context.Context, tx *sql.Tx, _, _ *memory.Memory) error {
		res, err := tx.ExecContext(ctx, `UPDATE reflection_changes SET undone_at = ? WHERE id = ? AND undone_at IS NULL`, now, changeID)
		if err != nil {
			return fmt.Errorf("record the undo: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return httpx.Conflict("that change was already undone")
		}
		// An undone merge holds only its own words again: it's no longer
		// linked to what it merged, and the record drops the merged text.
		if c.mergedFrom != "" {
			for _, src := range strings.Split(c.mergedFrom, ",") {
				if _, err := tx.ExecContext(ctx, `DELETE FROM memory_merges WHERE source_id = ? AND into_id = ?`, src, mid); err != nil {
					return fmt.Errorf("undo a merge: %w", err)
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE reflection_changes SET after = '' WHERE id = ?`, changeID); err != nil {
				return fmt.Errorf("undo a merge: %w", err)
			}
		}
		return nil
	}
	edited := httpx.Conflict("that memory was edited since; change it in Memories instead, so the newer edit isn't lost")
	switch c.Action {
	case ActionSave:
		if c.After == nil {
			return Change{}, httpx.Conflict("there's nothing recorded to undo")
		}
		_, err := s.mem.RetireIfUnchanged(ctx, mid, *c.After, mark)
		switch {
		case errors.Is(err, memory.ErrChanged):
			return Change{}, edited
		case isNotFound(err): // already gone: nothing left to undo but the record
			if _, err := s.db.ExecContext(ctx, `UPDATE reflection_changes SET undone_at = ? WHERE id = ? AND undone_at IS NULL`, now, changeID); err != nil {
				return Change{}, fmt.Errorf("record the undo: %w", err)
			}
		case err != nil:
			return Change{}, err
		}
	case ActionUpdate:
		if c.Before == nil || c.After == nil {
			return Change{}, httpx.Conflict("there's no earlier version to go back to")
		}
		b := c.Before
		follow := ""
		if b.FollowUpAt != nil {
			follow = *b.FollowUpAt
		}
		if _, err := s.mem.UpdateIfUnchanged(ctx, mid, *c.After, memory.Patch{Content: &b.Content, Kind: &b.Kind, Importance: &b.Importance, FollowUpAt: &follow}, mark); err != nil {
			switch {
			case errors.Is(err, memory.ErrChanged):
				return Change{}, edited
			case isNotFound(err):
				return Change{}, httpx.Conflict("that memory is gone now, so there's nothing to put back")
			}
			return Change{}, err
		}
	case ActionRetire:
		if _, err := s.mem.Restore(ctx, mid, mark); err != nil {
			if isNotFound(err) {
				return Change{}, httpx.Conflict("that memory was forgotten since, so it can't come back")
			}
			return Change{}, err
		}
	default:
		return Change{}, fmt.Errorf("unknown reflection change %q", c.Action)
	}
	c.UndoneAt = &now
	s.events.Invalidate(DomainJournal)
	return c, nil
}

func isNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}
