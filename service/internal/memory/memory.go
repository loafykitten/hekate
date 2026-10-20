// Package memory is what Renamon knows about the owner: a short always-loaded
// profile (core memory) and searchable notes with importance and follow-ups.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// Memory kinds from PROTOCOL.md.
var kinds = []string{"fact", "preference", "person", "event", "follow_up"}

// Limits.
const (
	maxContent     = 1000
	maxKey         = 40
	maxValue       = 400
	maxProfileKeys = 40
	defaultLimit   = 50
	maxLimit       = 200
	// DefaultImportance applies when a memory is saved without one.
	DefaultImportance = 3
	// followUpHour is when a date-only follow-up becomes due.
	followUpHour = 9
)

// Memory is the API shape from PROTOCOL.md.
type Memory struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Content    string  `json:"content"`
	Importance int     `json:"importance"`
	FollowUpAt *string `json:"follow_up_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// Entry is one line of the core profile.
type Entry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
}

// NewMemory is the input for Save.
type NewMemory struct {
	Kind       string
	Content    string
	Importance int    // 0: DefaultImportance
	FollowUpAt string // empty: none
	SourceTurn string
}

// Patch changes a memory. Nil fields stay; for FollowUpAt the empty string clears it.
type Patch struct {
	Content    *string
	Kind       *string
	Importance *int
	FollowUpAt *string
}

// Options are the dependencies of a Service.
type Options struct {
	Store    *store.Store
	Location *time.Location
	Events   proto.Events
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service owns memories and the core profile.
type Service struct {
	// mu serializes read-modify-write changes (REST and her tools overlap), so a
	// patch never writes back a stale copy of fields it didn't touch.
	mu     sync.Mutex
	db     *sql.DB
	loc    *time.Location
	events proto.Events
	log    *slog.Logger
	now    func() time.Time
}

// New builds the service.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("memory: Store is required")
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

const columns = `id, kind, content, importance, follow_up_at, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scan(row scanner) (Memory, error) {
	var (
		m                Memory
		id               int64
		followUp         sql.NullString
		created, updated string
	)
	if err := row.Scan(&id, &m.Kind, &m.Content, &m.Importance, &followUp, &created, &updated); err != nil {
		return Memory{}, err //nolint:wrapcheck // callers distinguish sql.ErrNoRows
	}
	m.ID = strconv.FormatInt(id, 10)
	if followUp.Valid {
		f := s.display(followUp.String)
		m.FollowUpAt = &f
	}
	m.CreatedAt, m.UpdatedAt = s.display(created), s.display(updated)
	return m, nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Service) display(stored string) string {
	t, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		return stored
	}
	return t.In(s.loc).Format(time.RFC3339)
}

func (s *Service) changed() { s.events.Invalidate(proto.DomainMemory) }

// Get returns one memory.
func (s *Service) Get(ctx context.Context, id int64) (Memory, error) {
	m, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM memories WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, httpx.NotFound("there is no memory with id %d", id)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("get memory %d: %w", id, err)
	}
	return m, nil
}

func checkKind(kind string) error {
	if !slices.Contains(kinds, kind) {
		return httpx.BadRequest("kind must be one of %s", strings.Join(kinds, ", "))
	}
	return nil
}

func checkContent(c string) (string, error) {
	c = strings.TrimSpace(c)
	switch {
	case c == "":
		return "", httpx.BadRequest("content is required")
	case utf8.RuneCountInString(c) > maxContent:
		return "", httpx.BadRequest("content is longer than %d characters", maxContent)
	}
	return c, nil
}

func checkImportance(n int) error {
	if n < 1 || n > 5 {
		return httpx.BadRequest("importance must be 1 (trivia) to 5 (must never forget)")
	}
	return nil
}

// followUp parses a follow-up time; a bare date means 9:00 that day.
func (s *Service) followUp(v string) (any, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil //nolint:nilnil // nil is the SQL NULL
	}
	p, err := when.Parse(v, s.loc)
	if err != nil {
		return nil, httpx.BadRequest("follow_up_at: %s", err)
	}
	t := p.T
	if p.DateOnly {
		t = time.Date(t.Year(), t.Month(), t.Day(), followUpHour, 0, 0, 0, s.loc)
	}
	return stamp(t), nil
}

// Save stores a memory. When the same text is already remembered it returns
// that memory, with importance raised if the new one is higher, and existed set.
func (s *Service) Save(ctx context.Context, n NewMemory) (m Memory, existed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkKind(n.Kind); err != nil {
		return Memory{}, false, err
	}
	content, err := checkContent(n.Content)
	if err != nil {
		return Memory{}, false, err
	}
	importance := n.Importance
	if importance == 0 {
		importance = DefaultImportance
	}
	if err := checkImportance(importance); err != nil {
		return Memory{}, false, err
	}
	followUp, err := s.followUp(n.FollowUpAt)
	if err != nil {
		return Memory{}, false, err
	}
	now := stamp(s.now())

	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM memories WHERE deleted_at IS NULL AND lower(content) = lower(?) LIMIT 1`, content).Scan(&id)
	switch {
	case err == nil:
		if _, err := s.db.ExecContext(ctx, `UPDATE memories SET importance = max(importance, ?),
			follow_up_at = COALESCE(?, follow_up_at), updated_at = ? WHERE id = ?`, importance, followUp, now, id); err != nil {
			return Memory{}, false, fmt.Errorf("refresh memory %d: %w", id, err)
		}
		existed = true
	case errors.Is(err, sql.ErrNoRows):
		res, err := s.db.ExecContext(ctx, `INSERT INTO memories (kind, content, importance, follow_up_at, source_turn_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, n.Kind, content, importance, followUp, n.SourceTurn, now, now)
		if err != nil {
			return Memory{}, false, fmt.Errorf("insert memory: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return Memory{}, false, fmt.Errorf("insert memory: %w", err)
		}
	default:
		return Memory{}, false, fmt.Errorf("look for a duplicate memory: %w", err)
	}
	s.changed()
	m, err = s.Get(ctx, id)
	return m, existed, err
}

// Update changes a memory.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	kind, content, importance := cur.Kind, cur.Content, cur.Importance
	if p.Kind != nil {
		if err := checkKind(*p.Kind); err != nil {
			return Memory{}, err
		}
		kind = *p.Kind
	}
	if p.Content != nil {
		if content, err = checkContent(*p.Content); err != nil {
			return Memory{}, err
		}
	}
	if p.Importance != nil {
		if err := checkImportance(*p.Importance); err != nil {
			return Memory{}, err
		}
		importance = *p.Importance
	}
	var followUp any
	if cur.FollowUpAt != nil {
		t, _ := time.Parse(time.RFC3339, *cur.FollowUpAt)
		followUp = stamp(t)
	}
	if p.FollowUpAt != nil {
		if followUp, err = s.followUp(*p.FollowUpAt); err != nil {
			return Memory{}, err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE memories SET kind = ?, content = ?, importance = ?, follow_up_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`, kind, content, importance, followUp, stamp(s.now()), id); err != nil {
		return Memory{}, fmt.Errorf("update memory %d: %w", id, err)
	}
	s.changed()
	return s.Get(ctx, id)
}

// Forget deletes a memory for good, including its search index entry.
func (s *Service) Forget(ctx context.Context, id int64) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.Get(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id); err != nil {
		return Memory{}, fmt.Errorf("forget memory %d: %w", id, err)
	}
	s.changed()
	return m, nil
}

// Retire hides a memory without deleting it, so the change can be undone
// (reflection retires stale, contradicted or merged memories this way).
func (s *Service) Retire(ctx context.Context, id int64) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.Get(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE memories SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, stamp(s.now()), id); err != nil {
		return Memory{}, fmt.Errorf("retire memory %d: %w", id, err)
	}
	s.changed()
	return m, nil
}

// Restore brings back a retired memory. A memory that was never retired is
// returned as it is; one that was forgotten is gone.
func (s *Service) Restore(ctx context.Context, id int64) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE memories SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`, stamp(s.now()), id)
	if err != nil {
		return Memory{}, fmt.Errorf("restore memory %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.changed()
	}
	return s.Get(ctx, id)
}

// List returns memories, newest first, optionally of one kind.
func (s *Service) List(ctx context.Context, kind string, limit int) ([]Memory, error) {
	if kind != "" {
		if err := checkKind(kind); err != nil {
			return nil, err
		}
	}
	return s.query(ctx, `SELECT `+columns+` FROM memories WHERE deleted_at IS NULL AND (? = '' OR kind = ?)
		ORDER BY updated_at DESC, id DESC LIMIT ?`, kind, kind, clampLimit(limit))
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	}
	return n
}

func (s *Service) query(ctx context.Context, q string, args ...any) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Memory{}
	for rows.Next() {
		m, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("query memories: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	return out, nil
}

// Search finds memories matching the words in text, best first. Ranking is
// BM25 weighted by importance, so what matters more wins between equally good
// matches. Text with no searchable words finds nothing.
func (s *Service) Search(ctx context.Context, text, kind string, limit int) ([]Memory, error) {
	if kind != "" {
		if err := checkKind(kind); err != nil {
			return nil, err
		}
	}
	match := MatchQuery(text)
	if match == "" {
		return []Memory{}, nil
	}
	// bm25 is negative (lower is better); scaling by importance makes important
	// memories more negative, i.e. better.
	return s.query(ctx, `SELECT m.id, m.kind, m.content, m.importance, m.follow_up_at, m.created_at, m.updated_at
		FROM memories_fts JOIN memories m ON m.id = memories_fts.rowid
		WHERE memories_fts MATCH ? AND m.deleted_at IS NULL AND (? = '' OR m.kind = ?)
		ORDER BY bm25(memories_fts) * (0.8 + 0.1 * m.importance), m.importance DESC, m.updated_at DESC, m.id DESC
		LIMIT ?`, match, kind, kind, clampLimit(limit))
}

// FollowUpsDue returns memories worth asking about by until, soonest first.
func (s *Service) FollowUpsDue(ctx context.Context, until time.Time, limit int) ([]Memory, error) {
	return s.query(ctx, `SELECT `+columns+` FROM memories WHERE deleted_at IS NULL AND follow_up_at IS NOT NULL AND follow_up_at <= ?
		ORDER BY follow_up_at, id LIMIT ?`, stamp(until), limit)
}

// Pinned returns the memories marked must-never-forget, most recently touched first.
func (s *Service) Pinned(ctx context.Context, limit int) ([]Memory, error) {
	return s.query(ctx, `SELECT `+columns+` FROM memories WHERE deleted_at IS NULL AND importance = 5
		ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
}

// Profile returns the core memory entries by key.
func (s *Service) Profile(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, updated_at FROM core_memory ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Key, &e.Value, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("read profile: %w", err)
		}
		e.UpdatedAt = s.display(e.UpdatedAt)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	return out, nil
}

// NormalizeKey lowercases a profile key and joins its words with underscores,
// so "Favorite Color" and "favorite_color" are one entry.
func NormalizeKey(key string) string {
	return strings.ToLower(strings.Join(strings.Fields(key), "_"))
}

// SetProfile adds or replaces a profile entry.
func (s *Service) SetProfile(ctx context.Context, key, value string) (Entry, error) {
	key, value = NormalizeKey(key), strings.TrimSpace(value)
	switch {
	case key == "":
		return Entry{}, httpx.BadRequest("key is required")
	case utf8.RuneCountInString(key) > maxKey:
		return Entry{}, httpx.BadRequest("key is longer than %d characters", maxKey)
	case value == "":
		return Entry{}, httpx.BadRequest("value is required (use profile_remove to delete an entry)")
	case utf8.RuneCountInString(value) > maxValue:
		return Entry{}, httpx.BadRequest("value is longer than %d characters; keep the profile short and put detail in a memory", maxValue)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM core_memory WHERE key <> ?`, key).Scan(&n); err != nil {
		return Entry{}, fmt.Errorf("count profile: %w", err)
	}
	if n >= maxProfileKeys {
		return Entry{}, httpx.Conflict("the profile is full (%d entries); remove one first", maxProfileKeys)
	}
	now := stamp(s.now())
	if _, err := s.db.ExecContext(ctx, `INSERT INTO core_memory (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now); err != nil {
		return Entry{}, fmt.Errorf("set profile %q: %w", key, err)
	}
	s.changed()
	return Entry{Key: key, Value: value, UpdatedAt: s.display(now)}, nil
}

// RemoveProfile deletes a profile entry.
func (s *Service) RemoveProfile(ctx context.Context, key string) error {
	key = NormalizeKey(key)
	res, err := s.db.ExecContext(ctx, `DELETE FROM core_memory WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("remove profile %q: %w", key, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.NotFound("the profile has no entry %q", key)
	}
	s.changed()
	return nil
}
