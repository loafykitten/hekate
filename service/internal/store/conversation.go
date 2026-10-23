package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Turn statuses.
const (
	TurnRunning     = "running"
	TurnDone        = "done"
	TurnInterrupted = "interrupted"
	TurnError       = "error"
)

// Message roles as stored.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one stored conversation message.
type Message struct {
	ID             int64
	ConversationID string
	TurnID         string
	Role           string
	Text           string // display text; empty for tool traffic
	Content        string // raw model-facing content
	ToolCalls      string // JSON array, assistant messages only
	ToolCallID     string // tool messages only
	Model          string
	CreatedAt      time.Time
}

// Turn is one user input and the reply to it.
type Turn struct {
	ID             string
	ConversationID string
	InputID        string
	Origin         string
	UserText       string
	StartedAt      time.Time
}

// Usage is the accounting for one LLM request.
type Usage struct {
	TurnID       string
	Model        string
	Tier         string
	InputTokens  int
	OutputTokens int
	CachedTokens int
	CostUSD      *float64
	LatencyMS    int64
	RequestID    string
}

// Activity is one tool call in the audit trail.
type Activity struct {
	TurnID     string
	Tool       string
	Effect     string
	Args       string
	Result     string
	Status     string
	DurationMS int64
}

// MaxActivityArgs caps the arguments kept in the audit trail.
const MaxActivityArgs = 2048

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// EnsureConversation creates the conversation if it does not exist.
func (s *Store) EnsureConversation(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (id, created_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`,
		id, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("ensure conversation: %w", err)
	}
	return nil
}

// AbortRunningTurns marks the conversation's turns left running by a crash or
// restart as errors.
func (s *Store) AbortRunningTurns(ctx context.Context, conversationID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE turns SET status = ?, error = 'service restarted', ended_at = ? WHERE status = ? AND conversation_id = ?`,
		TurnError, ts(time.Now()), TurnRunning, conversationID)
	if err != nil {
		return fmt.Errorf("abort running turns: %w", err)
	}
	return nil
}

// BeginTurn records a turn as running.
func (s *Store) BeginTurn(ctx context.Context, t Turn) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (id, conversation_id, input_id, origin, user_text, status, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ConversationID, t.InputID, t.Origin, t.UserText, TurnRunning, ts(t.StartedAt))
	if err != nil {
		return fmt.Errorf("begin turn: %w", err)
	}
	return nil
}

// EndTurn records how a turn finished.
func (s *Store) EndTurn(ctx context.Context, id, status, model, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE turns SET status = ?, model = ?, error = ?, ended_at = ? WHERE id = ?`,
		status, model, errMsg, ts(time.Now()), id)
	if err != nil {
		return fmt.Errorf("end turn: %w", err)
	}
	return nil
}

// AddMessages stores messages in order, in one transaction.
func (s *Store) AddMessages(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertMessages(ctx, tx, msgs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ReplaceTurnReply swaps a turn's stored assistant and tool messages for msgs,
// in one transaction. The user's message is kept.
func (s *Store) ReplaceTurnReply(ctx context.Context, turnID string, msgs []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM messages WHERE turn_id = ? AND role IN (?, ?)`, turnID, RoleAssistant, RoleTool); err != nil {
		return fmt.Errorf("delete reply: %w", err)
	}
	if err := insertMessages(ctx, tx, msgs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func insertMessages(ctx context.Context, tx *sql.Tx, msgs []Message) error {
	for _, m := range msgs {
		created := m.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages (conversation_id, turn_id, role, text, content, tool_calls, tool_call_id, model, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ConversationID, m.TurnID, m.Role, m.Text, m.Content, m.ToolCalls, m.ToolCallID, m.Model, ts(created)); err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
	}
	return nil
}

const messageCols = `id, conversation_id, turn_id, role, text, content, tool_calls, tool_call_id, model, created_at`

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.TurnID, &m.Role, &m.Text, &m.Content,
			&m.ToolCalls, &m.ToolCallID, &m.Model, &created); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read messages: %w", err)
	}
	return out, nil
}

// RecentMessages returns the newest limit messages (tool traffic included),
// oldest first, for rebuilding the model's context.
func (s *Store) RecentMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageCols+` FROM (
		   SELECT `+messageCols+` FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT ?
		 ) ORDER BY id ASC`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent messages: %w", err)
	}
	return scanMessages(rows)
}

// VisibleMessages returns user and assistant messages that have display text,
// older than message id before (0 means newest), newest last.
func (s *Store) VisibleMessages(ctx context.Context, conversationID string, before int64, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageCols+` FROM (
		   SELECT `+messageCols+` FROM messages
		   WHERE conversation_id = ? AND role IN ('user', 'assistant') AND text <> '' AND (? = 0 OR id < ?)
		   ORDER BY id DESC LIMIT ?
		 ) ORDER BY id ASC`, conversationID, before, before, limit)
	if err != nil {
		return nil, fmt.Errorf("visible messages: %w", err)
	}
	return scanMessages(rows)
}

// AddUsage records one LLM request.
func (s *Store) AddUsage(ctx context.Context, u Usage) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage (turn_id, model, tier, input_tokens, output_tokens, cached_tokens, cost_usd, latency_ms, request_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.TurnID, u.Model, u.Tier, u.InputTokens, u.OutputTokens, u.CachedTokens, u.CostUSD, u.LatencyMS, u.RequestID, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("add usage: %w", err)
	}
	return nil
}

// AddBackgroundUsage records an LLM request made outside a conversation turn;
// source names the work (mail, reflection). u.TurnID is ignored.
func (s *Store) AddBackgroundUsage(ctx context.Context, source string, u Usage) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO background_usage (source, model, tier, input_tokens, output_tokens, cached_tokens, cost_usd, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		source, u.Model, u.Tier, u.InputTokens, u.OutputTokens, u.CachedTokens, u.CostUSD, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("add background usage: %w", err)
	}
	return nil
}

// AddActivity records a tool call. Arguments are truncated to MaxActivityArgs.
func (s *Store) AddActivity(ctx context.Context, a Activity) error {
	if len(a.Args) > MaxActivityArgs {
		a.Args = truncateUTF8(a.Args, MaxActivityArgs)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO activity (turn_id, tool, effect, args, result, status, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.TurnID, a.Tool, a.Effect, a.Args, a.Result, a.Status, a.DurationMS, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("add activity: %w", err)
	}
	return nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
