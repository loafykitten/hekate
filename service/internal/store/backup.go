package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const backupsToKeep = 10

// Backup writes a consistent copy of the database to path (which must not exist).
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	// VACUUM INTO does not accept bound parameters, so escape the literal.
	lit := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO "+lit); err != nil { //nolint:gosec // G202: VACUUM INTO cannot take parameters; lit is quote-escaped
		return fmt.Errorf("vacuum into %s: %w", filepath.Base(path), err)
	}
	return nil
}

func (s *Store) preMigrateBackup(ctx context.Context, nextVersion int) error {
	dir := filepath.Join(s.dataDir, "backups")
	name := fmt.Sprintf("pre-migrate-%04d-%s.db", nextVersion, time.Now().UTC().Format("20060102T150405.000Z"))
	if err := s.Backup(ctx, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("pre-migration backup: %w", err)
	}
	return pruneBackups(dir, backupsToKeep)
}

// pruneBackups keeps the newest keep pre-migrate backups; names embed a UTC
// timestamp, so lexical order is chronological within one version width.
func pruneBackups(dir string, keep int) error {
	matches, err := filepath.Glob(filepath.Join(dir, "pre-migrate-*.db"))
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	// Sort by the timestamp suffix so a version bump doesn't reorder history.
	sort.Slice(matches, func(i, j int) bool { return stamp(matches[i]) < stamp(matches[j]) })
	for len(matches) > keep {
		if err := os.Remove(matches[0]); err != nil {
			return fmt.Errorf("remove old backup: %w", err)
		}
		matches = matches[1:]
	}
	return nil
}

func stamp(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), ".db")
	if i := strings.LastIndex(base, "-"); i >= 0 {
		return base[i+1:]
	}
	return base
}
