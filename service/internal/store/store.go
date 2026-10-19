// Package store owns the SQLite database: connection setup, migrations and backups.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const dbFile = "hekate.db"

// Store wraps the database handle.
type Store struct {
	db      *sql.DB
	dataDir string
}

// Open opens (creating if needed) <dataDir>/hekate.db and applies pending migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	return open(ctx, dataDir, migrations)
}

func open(ctx context.Context, dataDir string, migs []migration) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	// Pragmas go in the DSN so every pooled connection gets them.
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"foreign_keys(ON)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, dbFile)) + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, dataDir: dataDir}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := s.migrate(ctx, migs); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// DB exposes the handle for feature packages that add their own queries.
func (s *Store) DB() *sql.DB { return s.db }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
