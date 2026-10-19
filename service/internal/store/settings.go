package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// GetSetting returns the value for key, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("setting %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}
	return v, nil
}

// SetSetting upserts a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set setting %q: %w", key, err)
	}
	return nil
}

// GetSettingBool reads a boolean setting, returning def when unset.
func (s *Store) GetSettingBool(ctx context.Context, key string, def bool) (bool, error) {
	v, err := s.GetSetting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return def, nil
	}
	if err != nil {
		return false, err
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("setting %q: %w", key, err)
	}
	return b, nil
}

// SetSettingBool stores a boolean setting.
func (s *Store) SetSettingBool(ctx context.Context, key string, v bool) error {
	return s.SetSetting(ctx, key, strconv.FormatBool(v))
}

// GetSettingInt reads an integer setting, returning def when unset.
func (s *Store) GetSettingInt(ctx context.Context, key string, def int) (int, error) {
	v, err := s.GetSetting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return def, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("setting %q: %w", key, err)
	}
	return n, nil
}

// SetSettingInt stores an integer setting.
func (s *Store) SetSettingInt(ctx context.Context, key string, v int) error {
	return s.SetSetting(ctx, key, strconv.Itoa(v))
}
