package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
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
		version: 2, name: "extra", sql: `CREATE TABLE extra (id INTEGER PRIMARY KEY)`,
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
	if got := filepath.Base(b[0]); got[:len("pre-migrate-0002-")] != "pre-migrate-0002-" {
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
		version: 2, name: "bad", sql: `CREATE TABLE ok_table (id INTEGER); THIS IS NOT SQL;`,
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
	for i := 2; i < 16; i++ {
		migs := append([]migration{}, migrations...)
		for v := 2; v <= i; v++ {
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
