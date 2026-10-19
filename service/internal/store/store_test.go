package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// nextVersion is the version after the embedded migrations, for tests that add their own.
func nextVersion() int { return migrations[len(migrations)-1].version + 1 }

func backups(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPragmas(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for pragma, want := range map[string]string{
		"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "synchronous": "1",
	} {
		var got string
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", pragma, got, want)
		}
	}
}

func TestFreshOpenNoBackupAndIdempotent(t *testing.T) {
	_, dir := openTemp(t)
	if b := backups(t, dir); len(b) != 0 {
		t.Fatalf("fresh DB should not back up: %v", b)
	}
	// Reopening with nothing pending must not back up or fail.
	s2, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if b := backups(t, dir); len(b) != 0 {
		t.Fatalf("no-op reopen should not back up: %v", b)
	}
	var n int
	if err := s2.db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(migrations) {
		t.Fatalf("applied = %d, err = %v", n, err)
	}
}

func TestUpgradeCreatesBackup(t *testing.T) {
	ctx := context.Background()
	s, dir := openTemp(t)
	if err := s.SetSetting(ctx, "keep", "me"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	next := append(append([]migration{}, migrations...), migration{
		version: nextVersion(), name: "extra", sql: `CREATE TABLE extra (id INTEGER PRIMARY KEY)`,
	})
	s2, err := open(ctx, dir, next)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()

	b := backups(t, dir)
	if len(b) != 1 {
		t.Fatalf("backups = %v", b)
	}
	if got, want := filepath.Base(b[0]), fmt.Sprintf("pre-migrate-%04d-", nextVersion()); !strings.HasPrefix(got, want) {
		t.Errorf("name = %s", got)
	}
	// The backup holds pre-upgrade data and lacks the new table.
	bdb, err := sql.Open("sqlite", b[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bdb.Close() }()
	var extra int
	if err := bdb.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='extra'`).Scan(&extra); err != nil || extra != 0 {
		t.Fatalf("backup should predate migration: %d %v", extra, err)
	}
	var kept string
	if err := bdb.QueryRow(`SELECT value FROM settings WHERE key='keep'`).Scan(&kept); err != nil || kept != "me" {
		t.Fatalf("backup missing data: %v", err)
	}
	var v string
	if err := s2.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='keep'`).Scan(&v); err != nil || v != "me" {
		t.Fatalf("data lost: %v %q", err, v)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	s, dir := openTemp(t)
	_ = s.Close()
	bad := append(append([]migration{}, migrations...), migration{
		version: nextVersion(), name: "bad", sql: `CREATE TABLE ok_table (id INTEGER); THIS IS NOT SQL;`,
	})
	if _, err := open(ctx, dir, bad); err == nil {
		t.Fatal("expected error")
	}
	s2, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	var n int
	_ = s2.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='ok_table'`).Scan(&n)
	if n != 0 {
		t.Fatal("partial migration was committed")
	}
}

func TestBackupRetention(t *testing.T) {
	ctx := context.Background()
	s, dir := openTemp(t)
	_ = s.Close()
	base := nextVersion()
	for i := base; i < base+14; i++ {
		migs := append([]migration{}, migrations...)
		for v := base; v <= i; v++ {
			migs = append(migs, migration{version: v, name: "t", sql: fmt.Sprintf("CREATE TABLE t%d (id INTEGER)", v)})
		}
		st, err := open(ctx, dir, migs)
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
	}
	if b := backups(t, dir); len(b) != backupsToKeep {
		t.Fatalf("backups = %d, want %d", len(b), backupsToKeep)
	}
}

func TestManualBackup(t *testing.T) {
	ctx := context.Background()
	s, dir := openTemp(t)
	if err := s.SetSetting(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nightly", "it's.db") // quote exercises escaping
	if err := s.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.GetSetting(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := s.SetSetting(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting(ctx, "k"); v != "v2" {
		t.Fatalf("v = %q", v)
	}
	if b, err := s.GetSettingBool(ctx, "flag", true); err != nil || !b {
		t.Fatalf("default bool: %v %v", b, err)
	}
	_ = s.SetSettingBool(ctx, "flag", false)
	if b, _ := s.GetSettingBool(ctx, "flag", true); b {
		t.Fatal("bool not stored")
	}
	_ = s.SetSettingInt(ctx, "n", 42)
	if n, _ := s.GetSettingInt(ctx, "n", 0); n != 42 {
		t.Fatalf("n = %d", n)
	}
	_ = s.SetSetting(ctx, "bad", "xyz")
	if _, err := s.GetSettingInt(ctx, "bad", 0); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestFTS5Available(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `CREATE VIRTUAL TABLE docs USING fts5(body)`); err != nil {
		t.Fatalf("FTS5 unavailable: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO docs(body) VALUES ('hello brave new world')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM docs WHERE docs MATCH 'brave'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("match: n=%d err=%v", n, err)
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	good := fstest.MapFS{"m/0001_a.sql": {Data: []byte("SELECT 1")}, "m/0002_b.sql": {Data: []byte("SELECT 1")}}
	if got, err := loadMigrations(good, "m"); err != nil || len(got) != 2 || got[1].name != "b" {
		t.Fatalf("got %v %v", got, err)
	}
	for name, fsys := range map[string]fstest.MapFS{
		"bad name":  {"m/oops.sql": {Data: []byte("x")}},
		"no number": {"m/abc_x.sql": {Data: []byte("x")}},
		"duplicate": {"m/0001_a.sql": {Data: []byte("x")}, "m/1_b.sql": {Data: []byte("x")}},
	} {
		if _, err := loadMigrations(fsys, "m"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestConversationRoundTrip(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if err := s.EnsureConversation(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureConversation(ctx, "default"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if err := s.BeginTurn(ctx, Turn{ID: "t1", ConversationID: "default", UserText: "hi", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	err = s.AddMessages(ctx, []Message{
		{ConversationID: "default", TurnID: "t1", Role: RoleUser, Text: "hi", Content: "hi"},
		{ConversationID: "default", TurnID: "t1", Role: RoleAssistant, ToolCalls: `[{"id":"c"}]`},
		{ConversationID: "default", TurnID: "t1", Role: RoleTool, Content: "result", ToolCallID: "c"},
		{ConversationID: "default", TurnID: "t1", Role: RoleAssistant, Text: "Hello.", Content: "{waving} Hello.", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}

	recent, err := s.RecentMessages(ctx, "default", 3)
	if err != nil || len(recent) != 3 || recent[0].Role != RoleAssistant || recent[2].Content != "{waving} Hello." {
		t.Fatalf("recent = %+v err %v", recent, err)
	}
	vis, err := s.VisibleMessages(ctx, "default", 0, 10)
	if err != nil || len(vis) != 2 || vis[0].Text != "hi" || vis[1].Text != "Hello." {
		t.Fatalf("visible = %+v err %v", vis, err)
	}
	older, err := s.VisibleMessages(ctx, "default", vis[1].ID, 10)
	if err != nil || len(older) != 1 || older[0].Text != "hi" {
		t.Fatalf("before = %+v err %v", older, err)
	}
	if err := s.EndTurn(ctx, "t1", TurnDone, "m", ""); err != nil {
		t.Fatal(err)
	}

	cost := 0.5
	if err := s.AddUsage(ctx, Usage{TurnID: "t1", Model: "m", CostUSD: &cost, InputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, Usage{TurnID: "t1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM usage WHERE cost_usd IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("null costs = %d err %v", nulls, err)
	}

	big := strings.Repeat("é", 3000)
	if err := s.AddActivity(ctx, Activity{TurnID: "t1", Tool: "x", Effect: "read", Args: big, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	var args string
	if err := s.DB().QueryRowContext(ctx, `SELECT args FROM activity`).Scan(&args); err != nil || len(args) > MaxActivityArgs || !utf8.ValidString(args) {
		t.Fatalf("args len %d valid %v err %v", len(args), utf8.ValidString(args), err)
	}

	if err := s.BeginTurn(ctx, Turn{ID: "t2", ConversationID: "default", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortRunningTurns(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM turns WHERE id = 't2'`).Scan(&status); err != nil || status != TurnError {
		t.Fatalf("status = %q err %v", status, err)
	}
}
