package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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
	RulesApplied bool
}

const rowCols = `id, account, folder, uidvalidity, uid, message_id, from_name, from_addr, subject, date, unread,
	category, gist, list_id, unsub_url, has_post, unsubscribed_at IS NOT NULL, gone, rules_applied`

type scanner interface{ Scan(dest ...any) error }

func scanRow(sc scanner) (row, error) {
	var r row
	err := sc.Scan(&r.ID, &r.Account, &r.Folder, &r.Validity, &r.UID, &r.MessageID, &r.FromName, &r.FromAddr, &r.Subject,
		&r.Date, &r.Unread, &r.Category, &r.Gist, &r.ListID, &r.UnsubURL, &r.HasPost, &r.Unsubscribed, &r.Gone, &r.RulesApplied)
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
	AllFolders bool // false: the inbox only (unless Folder says otherwise)
	// Folder narrows by kind of folder: inbox, archive, trash or all. Empty
	// falls back to AllFolders.
	Folder     string
	UnreadOnly bool
	Days       int
	Limit      int
	Offset     int
}

// Folder kinds a search can be scoped to.
const (
	folderInbox   = "inbox"
	folderArchive = "archive"
	folderTrash   = "trash"
	folderAll     = "all"
	folderOther   = "other"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Messages lists cached messages, newest first.
func (s *Service) Messages(ctx context.Context, q Query) ([]Message, error) {
	if !s.Configured() {
		return []Message{}, nil
	}
	where, args, empty, err := s.searchWhere(&q)
	if err != nil {
		return nil, err
	}
	if empty {
		return []Message{}, nil
	}
	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	q.Limit = min(q.Limit, maxLimit)
	args = append(args, q.Limit, max(q.Offset, 0))
	// The conditions are fixed strings chosen in searchWhere; every value is a bound argument.
	//nolint:gosec // see above
	query := `SELECT ` + rowCols + ` FROM mail_messages m
		WHERE ` + where + ` ORDER BY m.date DESC, m.id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search mail: %w", err)
	}
	return collect(rows)
}

// FolderCounts counts the messages a query matches in each kind of folder
// (inbox, archive, trash, other), ignoring its folder scope, limit and offset.
func (s *Service) FolderCounts(ctx context.Context, q Query) (map[string]int, error) {
	out := map[string]int{}
	if !s.Configured() {
		return out, nil
	}
	q.Folder = folderAll
	where, args, empty, err := s.searchWhere(&q)
	if err != nil || empty {
		return out, err
	}
	// The conditions are fixed strings chosen in searchWhere; every value is a bound argument.
	//nolint:gosec // see above
	rows, err := s.db.QueryContext(ctx, `SELECT m.account, m.folder, COUNT(*) FROM mail_messages m WHERE `+where+` GROUP BY 1, 2`, args...)
	if err != nil {
		return nil, fmt.Errorf("count mail: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var account, folder string
		var n int
		if err := rows.Scan(&account, &folder, &n); err != nil {
			return nil, fmt.Errorf("count mail: %w", err)
		}
		out[s.folderKind(account, folder)] += n
	}
	return out, rows.Err()
}

// Count is how many messages a query matches, ignoring its limit and offset.
func (s *Service) Count(ctx context.Context, q Query) (int, error) {
	if !s.Configured() {
		return 0, nil
	}
	where, args, empty, err := s.searchWhere(&q)
	if err != nil || empty {
		return 0, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_messages m WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count mail: %w", err)
	}
	return n, nil
}

// isTrashName reports whether a folder name's last path segment is a
// well-known trash name ("Trash", "[Gmail]/Trash", "INBOX.Deleted Items").
func isTrashName(folder string) bool {
	name := strings.ToLower(folder)
	if i := strings.LastIndexAny(name, "/."); i >= 0 {
		name = name[i+1:]
	}
	return slices.Contains(trashNames, name)
}

// isArchiveName reports whether folder is the account's archive folder.
func (s *Service) isArchiveName(account, folder string) bool {
	a := s.byName[account]
	return a != nil && folder != "" && strings.EqualFold(folder, a.cfg.Archive)
}

// folderKind sorts a cached folder into inbox, archive, trash or other.
func (s *Service) folderKind(account, folder string) string {
	switch {
	case folder == inbox:
		return folderInbox
	case s.isArchiveName(account, folder):
		return folderArchive
	case isTrashName(folder):
		return folderTrash
	}
	return folderOther
}

// searchWhere builds the WHERE clause (without LIMIT) for a query. empty is
// true when the query cannot match anything.
func (s *Service) searchWhere(q *Query) (where string, args []any, empty bool, err error) {
	if q.Category != "" && q.Category != unclassified && !validCategory(q.Category) {
		return "", nil, false, httpx.BadRequest("category must be one of %s or %s", strings.Join(categories, ", "), unclassified)
	}
	if q.Account != "" && s.byName[q.Account] == nil {
		return "", nil, false, httpx.BadRequest("no mail account named %q", q.Account)
	}
	conds := []string{"m.gone = 0"}
	folder := strings.ToLower(strings.TrimSpace(q.Folder))
	if folder == "" {
		folder = folderInbox
		if q.AllFolders {
			folder = folderAll
		}
	}
	switch folder {
	case folderAll:
	case folderInbox:
		conds = append(conds, "m.folder = 'INBOX'")
	case folderArchive:
		var parts []string
		for _, a := range s.accounts {
			parts = append(parts, "(m.account = ? AND LOWER(m.folder) = LOWER(?))")
			args = append(args, a.cfg.Name, a.cfg.Archive)
		}
		if len(parts) == 0 {
			return "", nil, true, nil
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	case folderTrash:
		var parts []string
		for _, n := range trashNames {
			parts = append(parts, "LOWER(m.folder) = ?", "LOWER(m.folder) LIKE ?", "LOWER(m.folder) LIKE ?")
			args = append(args, n, "%/"+n, "%."+n)
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	default:
		return "", nil, false, httpx.BadRequest("folder must be inbox, archive, trash or all")
	}
	if q.Text != "" {
		fts := ftsQuery(q.Text)
		if fts == "" {
			return "", nil, true, nil
		}
		conds = append(conds, "m.id IN (SELECT rowid FROM mail_messages_fts WHERE mail_messages_fts MATCH ?)")
		args = append(args, fts)
	}
	if q.From != "" {
		from := strings.ToLower(strings.TrimSpace(q.From))
		switch {
		case strings.HasPrefix(from, "@"):
			conds = append(conds, "(m.from_addr LIKE ? ESCAPE '\\' OR m.from_addr LIKE ? ESCAPE '\\')")
			args = append(args, "%"+likeEscape(from), "%."+likeEscape(from[1:]))
		case strings.Contains(from, "@"):
			conds = append(conds, "m.from_addr = ?")
			args = append(args, from)
		default:
			conds = append(conds, "(m.from_addr LIKE ? ESCAPE '\\' OR LOWER(m.from_name) LIKE ? ESCAPE '\\')")
			args = append(args, "%"+likeEscape(from)+"%", "%"+likeEscape(from)+"%")
		}
	}
	switch q.Category {
	case "":
	case unclassified:
		conds = append(conds, "m.category IS NULL")
	default:
		conds = append(conds, "m.category = ?")
		args = append(args, q.Category)
	}
	if q.Account != "" {
		conds = append(conds, "m.account = ?")
		args = append(args, q.Account)
	}
	if q.UnreadOnly {
		conds = append(conds, "m.unread = 1")
	}
	if q.Days > 0 {
		conds = append(conds, "m.date >= ?")
		args = append(args, stamp(s.now().AddDate(0, 0, -q.Days)))
	}
	return strings.Join(conds, " AND "), args, false, nil
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

// ftsQuery turns free text into a safe FTS5 query. Words must all match; an
// uppercase OR between terms makes them alternatives ("eleven labs OR
// elevenlabs" finds either). The last word of each alternative is a prefix, so
// partial words work while typing.
func ftsQuery(text string) string {
	var groups, words []string
	flush := func() {
		if len(words) > 0 {
			words[len(words)-1] += "*"
			groups = append(groups, strings.Join(words, " "))
		}
		words = nil
	}
	for _, tok := range strings.Fields(text) {
		if tok == "OR" {
			flush()
			continue
		}
		for _, w := range strings.FieldsFunc(tok, func(r rune) bool { return !isWordRune(r) }) {
			words = append(words, `"`+w+`"`)
		}
	}
	flush()
	return strings.Join(groups, " OR ")
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

// bindAccounts ties each account's cache to the mailbox it came from. When an
// account keeps its label but points at a different user or host, its cached
// messages are dropped: their ids would otherwise reach into the new mailbox,
// and a pending approval could act on an unrelated message there. Ids are
// never reused, so approvals for the old messages find nothing.
//
// Labels no longer configured (an account removed or renamed) lose their
// cache too, so stale copies never mix into counts, lists or the noise report.
func (s *Service) bindAccounts(ctx context.Context) error {
	now := stamp(s.now())
	if err := s.dropUnconfigured(ctx); err != nil {
		return err
	}
	for _, a := range s.accounts {
		identity := strings.ToLower(a.cfg.User) + "|" + strings.ToLower(a.cfg.Host)
		var stored string
		err := s.db.QueryRowContext(ctx, `SELECT identity FROM mail_accounts WHERE name = ?`, a.cfg.Name).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := s.db.ExecContext(ctx, `INSERT INTO mail_accounts (name, identity, updated_at) VALUES (?, ?, ?)`,
				a.cfg.Name, identity, now); err != nil {
				return fmt.Errorf("mail: record account %q: %w", a.cfg.Name, err)
			}
			continue
		case err != nil:
			return fmt.Errorf("mail: load account %q: %w", a.cfg.Name, err)
		case stored == identity:
			continue
		}
		s.log.Warn("mail account now points at a different mailbox; clearing its cache", "account", a.cfg.Name)
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("mail: reset account %q: %w", a.cfg.Name, err)
		}
		_, err1 := tx.ExecContext(ctx, `DELETE FROM mail_messages WHERE account = ?`, a.cfg.Name)
		_, err2 := tx.ExecContext(ctx, `UPDATE mail_accounts SET identity = ?, uidvalidity = 0, last_uid = 0, last_sweep_at = NULL,
			last_error = '', updated_at = ? WHERE name = ?`, identity, now, a.cfg.Name)
		if err := errors.Join(err1, err2); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mail: reset account %q: %w", a.cfg.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("mail: reset account %q: %w", a.cfg.Name, err)
		}
	}
	return nil
}

// dropUnconfigured removes the cache and state of account labels that are no
// longer configured.
func (s *Service) dropUnconfigured(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT account FROM mail_messages UNION SELECT name FROM mail_accounts`)
	if err != nil {
		return fmt.Errorf("mail: list cached accounts: %w", err)
	}
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mail: list cached accounts: %w", err)
		}
		if s.byName[name] == nil {
			stale = append(stale, name)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("mail: list cached accounts: %w", err)
	}
	for _, name := range stale {
		s.log.Info("dropping cached mail of an account no longer configured", "account", name)
		_, err1 := s.db.ExecContext(ctx, `DELETE FROM mail_messages WHERE account = ?`, name)
		_, err2 := s.db.ExecContext(ctx, `DELETE FROM mail_accounts WHERE name = ?`, name)
		if err := errors.Join(err1, err2); err != nil {
			return fmt.Errorf("mail: drop account %q: %w", name, err)
		}
	}
	return nil
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
	_, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET gone = 1, folder = '', uid = 0,
		left_inbox_at = COALESCE(left_inbox_at, ?) WHERE id = ?`, stamp(s.now()), id)
	if err != nil {
		s.log.Error("cannot mark mail gone", "id", id, "error", err)
	}
}

// markGoneAt marks a row gone only if it is still where r says, so a row that
// was relocated in the meantime keeps its new place.
func (s *Service) markGoneAt(ctx context.Context, r row) {
	_, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET gone = 1, folder = '', uid = 0,
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
