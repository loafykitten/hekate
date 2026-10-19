package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Address is a sender, as the API shows it.
type Address struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Message is the API shape from PROTOCOL.md.
type Message struct {
	ID             string  `json:"id"`
	Account        string  `json:"account"`
	From           Address `json:"from"`
	Subject        string  `json:"subject"`
	Date           string  `json:"date"`
	Folder         string  `json:"folder"`
	Unread         bool    `json:"unread"`
	Category       *string `json:"category"`
	Gist           *string `json:"gist"`
	CanUnsubscribe bool    `json:"can_unsubscribe"`
}

// row is a cached message as stored.
type row struct {
	ID           int64
	Account      string
	Folder       string // empty when it left the inbox for somewhere unknown
	Validity     uint32
	UID          uint32
	MessageID    string
	FromName     string
	FromAddr     string
	Subject      string
	Date         string
	Unread       bool
	Category     sql.NullString
	Gist         sql.NullString
	ListID       string
	UnsubURL     string
	HasPost      bool
	Unsubscribed bool
	Gone         bool
}

const rowCols = `id, account, folder, uidvalidity, uid, message_id, from_name, from_addr, subject, date, unread,
	category, gist, list_id, unsub_url, has_post, unsubscribed_at IS NOT NULL, gone`

type scanner interface{ Scan(dest ...any) error }

func scanRow(sc scanner) (row, error) {
	var r row
	err := sc.Scan(&r.ID, &r.Account, &r.Folder, &r.Validity, &r.UID, &r.MessageID, &r.FromName, &r.FromAddr, &r.Subject,
		&r.Date, &r.Unread, &r.Category, &r.Gist, &r.ListID, &r.UnsubURL, &r.HasPost, &r.Unsubscribed, &r.Gone)
	return r, err
}

// message renders a row for the API.
func (r row) message() Message {
	m := Message{
		ID: strconv.FormatInt(r.ID, 10), Account: r.Account, From: Address{Name: r.FromName, Address: r.FromAddr},
		Subject: r.Subject, Date: r.Date, Folder: r.Folder, Unread: r.Unread, CanUnsubscribe: r.canUnsubscribe(),
	}
	if r.Folder == "" {
		m.Folder = "other"
	}
	if r.Category.Valid {
		m.Category = &r.Category.String
	}
	if r.Gist.Valid {
		m.Gist = &r.Gist.String
	}
	return m
}

func (r row) canUnsubscribe() bool { return r.UnsubURL != "" && r.HasPost && !r.Unsubscribed }

func (s *Service) rowsByID(ctx context.Context, ids []int64) ([]row, error) {
	out := make([]row, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := scanRow(s.db.QueryRowContext(ctx, `SELECT `+rowCols+` FROM mail_messages WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNoMessage(id)
		}
		if err != nil {
			return nil, fmt.Errorf("load message %d: %w", id, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) rowByID(ctx context.Context, id int64) (row, error) {
	rs, err := s.rowsByID(ctx, []int64{id})
	if err != nil {
		return row{}, err
	}
	return rs[0], nil
}

// Message returns one cached message.
func (s *Service) Message(ctx context.Context, id int64) (Message, error) {
	r, err := s.rowByID(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return r.message(), nil
}

// Query narrows a message listing.
type Query struct {
	Text       string // sender, subject and gist
	From       string // an address or @domain
	Category   string
	Account    string
	AllFolders bool // false: the inbox only
	UnreadOnly bool
	Days       int
	Limit      int
}

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Messages lists cached messages, newest first.
func (s *Service) Messages(ctx context.Context, q Query) ([]Message, error) {
	if !s.Configured() {
		return []Message{}, nil
	}
	if q.Category != "" && q.Category != unclassified && !validCategory(q.Category) {
		return nil, httpx.BadRequest("category must be one of %s or %s", strings.Join(categories, ", "), unclassified)
	}
	if q.Account != "" && s.byName[q.Account] == nil {
		return nil, httpx.BadRequest("no mail account named %q", q.Account)
	}
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	q.Limit = min(q.Limit, maxLimit)

	var where []string
	var args []any
	if q.AllFolders {
		where = append(where, "m.gone = 0")
	} else {
		where = append(where, "m.folder = 'INBOX' AND m.gone = 0")
	}
	if q.Text != "" {
		fts := ftsQuery(q.Text)
		if fts == "" {
			return []Message{}, nil
		}
		where = append(where, "m.id IN (SELECT rowid FROM mail_messages_fts WHERE mail_messages_fts MATCH ?)")
		args = append(args, fts)
	}
	if q.From != "" {
		from := strings.ToLower(strings.TrimSpace(q.From))
		switch {
		case strings.HasPrefix(from, "@"):
			where = append(where, "(m.from_addr LIKE ? ESCAPE '\\' OR m.from_addr LIKE ? ESCAPE '\\')")
			args = append(args, "%"+likeEscape(from), "%."+likeEscape(from[1:]))
		case strings.Contains(from, "@"):
			where = append(where, "m.from_addr = ?")
			args = append(args, from)
		default:
			where = append(where, "(m.from_addr LIKE ? ESCAPE '\\' OR LOWER(m.from_name) LIKE ? ESCAPE '\\')")
			args = append(args, "%"+likeEscape(from)+"%", "%"+likeEscape(from)+"%")
		}
	}
	switch q.Category {
	case "":
	case unclassified:
		where = append(where, "m.category IS NULL")
	default:
		where = append(where, "m.category = ?")
		args = append(args, q.Category)
	}
	if q.Account != "" {
		where = append(where, "m.account = ?")
		args = append(args, q.Account)
	}
	if q.UnreadOnly {
		where = append(where, "m.unread = 1")
	}
	if q.Days > 0 {
		where = append(where, "m.date >= ?")
		args = append(args, stamp(s.now().AddDate(0, 0, -q.Days)))
	}
	args = append(args, q.Limit)
	// The conditions are fixed strings chosen above; every value is a bound argument.
	//nolint:gosec // see above
	query := `SELECT ` + rowCols + ` FROM mail_messages m
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY m.date DESC, m.id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search mail: %w", err)
	}
	return collect(rows)
}

func collect(rows *sql.Rows) ([]Message, error) {
	defer func() { _ = rows.Close() }()
	out := []Message{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("read mail: %w", err)
		}
		out = append(out, r.message())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read mail: %w", err)
	}
	return out, nil
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ftsQuery turns free text into a safe FTS5 query: every word must match, the
// last as a prefix, so partial words work while typing.
func ftsQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool { return !isWordRune(r) })
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	if len(words) == 0 {
		return ""
	}
	words[len(words)-1] += "*"
	return strings.Join(words, " ")
}

func isWordRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127
}

// Summary is the unread picture: counts by category and what needs attention.
type Summary struct {
	Unread    map[string]int `json:"unread"`
	Attention []Message      `json:"attention"`
}

// unclassified is the key under which unread mail with no category is counted.
const unclassified = "unclassified"

// Summary counts unread inbox mail by category and lists up to limit unread
// needs_reply and important messages, newest first.
func (s *Service) Summary(ctx context.Context, limit int) (Summary, error) {
	out := Summary{Unread: map[string]int{}, Attention: []Message{}}
	if !s.Configured() {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(category, ?), COUNT(*) FROM mail_messages
		WHERE folder = 'INBOX' AND gone = 0 AND unread = 1 GROUP BY 1`, unclassified)
	if err != nil {
		return out, fmt.Errorf("count unread mail: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cat string
		var n int
		if err := rows.Scan(&cat, &n); err != nil {
			return out, fmt.Errorf("count unread mail: %w", err)
		}
		out.Unread[cat] = n
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("count unread mail: %w", err)
	}
	att, err := s.db.QueryContext(ctx, `SELECT `+rowCols+` FROM mail_messages
		WHERE folder = 'INBOX' AND gone = 0 AND unread = 1 AND category IN ('needs_reply', 'important')
		ORDER BY date DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return out, fmt.Errorf("list attention mail: %w", err)
	}
	out.Attention, err = collect(att)
	return out, err
}

// accountState is what the sweep remembers about an account.
type accountState struct {
	validity uint32
	lastUID  uint32
	sweepAt  sql.NullString
	err      string
}

func (s *Service) accountState(ctx context.Context, name string) (accountState, error) {
	var st accountState
	err := s.db.QueryRowContext(ctx, `SELECT uidvalidity, last_uid, last_sweep_at, last_error FROM mail_accounts WHERE name = ?`, name).
		Scan(&st.validity, &st.lastUID, &st.sweepAt, &st.err)
	if errors.Is(err, sql.ErrNoRows) {
		return accountState{}, nil
	}
	if err != nil {
		return st, fmt.Errorf("load mail account state: %w", err)
	}
	return st, nil
}

// saveSync records a successful sync.
func (s *Service) saveSync(ctx context.Context, name string, validity, lastUID uint32) error {
	now := stamp(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO mail_accounts (name, uidvalidity, last_uid, last_sweep_at, last_error, updated_at)
		VALUES (?, ?, ?, ?, '', ?)
		ON CONFLICT(name) DO UPDATE SET uidvalidity = excluded.uidvalidity, last_uid = excluded.last_uid,
			last_sweep_at = excluded.last_sweep_at, last_error = '', updated_at = excluded.updated_at`,
		name, validity, lastUID, now, now)
	if err != nil {
		return fmt.Errorf("save mail account state: %w", err)
	}
	return nil
}

// saveError records why the last sweep of an account failed.
func (s *Service) saveError(ctx context.Context, name, msg string) {
	now := stamp(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO mail_accounts (name, last_error, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET last_error = excluded.last_error, updated_at = excluded.updated_at`,
		name, msg, now)
	if err != nil {
		s.log.Error("cannot record a mail error", "account", name, "error", err)
	}
}

// AccountStatus is one account in the status response.
type AccountStatus struct {
	Name        string  `json:"name"`
	Address     string  `json:"address"`
	OK          bool    `json:"ok"`
	Error       *string `json:"error"`
	LastSweepAt *string `json:"last_sweep_at"`
	InboxCount  int     `json:"inbox_count"`
}

// Status reports each account's health.
func (s *Service) Status(ctx context.Context) ([]AccountStatus, error) {
	out := make([]AccountStatus, 0, len(s.accounts))
	for _, a := range s.accounts {
		st, err := s.accountState(ctx, a.cfg.Name)
		if err != nil {
			return nil, err
		}
		as := AccountStatus{Name: a.cfg.Name, Address: a.cfg.User, OK: st.err == ""}
		if st.err != "" {
			e := st.err
			as.Error = &e
		}
		if st.sweepAt.Valid {
			as.LastSweepAt = &st.sweepAt.String
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_messages WHERE account = ? AND folder = 'INBOX' AND gone = 0`,
			a.cfg.Name).Scan(&as.InboxCount); err != nil {
			return nil, fmt.Errorf("count inbox: %w", err)
		}
		out = append(out, as)
	}
	return out, nil
}

// pruneLeft forgets rows for mail that left the inbox long ago.
func (s *Service) pruneLeft(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mail_messages WHERE left_inbox_at IS NOT NULL AND left_inbox_at < ?`,
		stamp(s.now().Add(-retainGone)))
	if err != nil {
		return fmt.Errorf("prune mail cache: %w", err)
	}
	return nil
}

// setUnread updates the cached read state.
func (s *Service) setUnread(ctx context.Context, id int64, unread bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET unread = ? WHERE id = ?`, unread, id)
	if err != nil {
		return fmt.Errorf("update mail: %w", err)
	}
	return nil
}

// markGone records that a message is no longer where Hekate last saw it.
func (s *Service) markGone(ctx context.Context, id int64) {
	_, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET gone = 1, folder = '',
		left_inbox_at = COALESCE(left_inbox_at, ?) WHERE id = ?`, stamp(s.now()), id)
	if err != nil {
		s.log.Error("cannot mark mail gone", "id", id, "error", err)
	}
}

// markGoneAt marks a row gone only if it is still where r says, so a row that
// was relocated in the meantime keeps its new place.
func (s *Service) markGoneAt(ctx context.Context, r row) {
	_, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET gone = 1, folder = '',
		left_inbox_at = COALESCE(left_inbox_at, ?) WHERE id = ? AND folder = ? AND uidvalidity = ? AND uid = ?`,
		stamp(s.now()), r.ID, r.Folder, r.Validity, r.UID)
	if err != nil {
		s.log.Error("cannot mark mail gone", "id", r.ID, "error", err)
	}
}

// relocate points a row at its new place after a move, keeping its id.
func (s *Service) relocate(ctx context.Context, id int64, folder string, validity, uid uint32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update mail: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if uid > 0 {
		// A stale row for the same server message would break the unique key.
		if _, err := tx.ExecContext(ctx, `DELETE FROM mail_messages WHERE id <> ? AND account = (SELECT account FROM mail_messages WHERE id = ?)
			AND folder = ? AND uidvalidity = ? AND uid = ?`, id, id, folder, validity, uid); err != nil {
			return fmt.Errorf("update mail: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mail_messages SET folder = ?, uidvalidity = ?, uid = ?, gone = 0,
		left_inbox_at = CASE WHEN ? = 'INBOX' THEN NULL ELSE COALESCE(left_inbox_at, ?) END, rules_applied = 1 WHERE id = ?`,
		folder, validity, uid, folder, stamp(s.now()), id); err != nil {
		return fmt.Errorf("update mail: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update mail: %w", err)
	}
	return nil
}
