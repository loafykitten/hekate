// Package config loads and validates service configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Secret is a string that never renders its value in logs or fmt output.
// Later milestones add API keys as Secret fields on Config.
type Secret string

const redacted = "[redacted]"

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer so %#v cannot leak either.
func (Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText keeps secrets out of JSON and other encoders.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal returns the underlying value; call it only at the point of use.
func (s Secret) Reveal() string { return string(s) }

// Config is the typed service configuration.
type Config struct {
	Addr      string
	DataDir   string
	WebDir    string // optional: built PWA
	AssetsDir string // optional: character assets
	LogLevel  slog.Level
	TamerName string
	Location  *time.Location
}

// Load reads configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration through getenv, which keeps tests hermetic.
func LoadFrom(getenv func(string) string) (Config, error) {
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Addr:      str("HEKATE_ADDR", "127.0.0.1:3200"),
		DataDir:   str("HEKATE_DATA_DIR", "./data"),
		WebDir:    str("HEKATE_WEB_DIR", ""),
		AssetsDir: str("HEKATE_ASSETS_DIR", ""),
		TamerName: str("HEKATE_TAMER_NAME", "Tamer"),
	}

	var errs []error

	if err := cfg.LogLevel.UnmarshalText([]byte(str("HEKATE_LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("HEKATE_LOG_LEVEL: %w", err))
	} else if lvl := strings.ToLower(str("HEKATE_LOG_LEVEL", "info")); lvl != "debug" && lvl != "info" && lvl != "warn" && lvl != "error" {
		// slog also accepts forms like "INFO+2"; keep the surface small.
		errs = append(errs, fmt.Errorf("HEKATE_LOG_LEVEL: %q is not one of debug, info, warn, error", lvl))
	}

	if tz := str("HEKATE_TIMEZONE", ""); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			errs = append(errs, fmt.Errorf("HEKATE_TIMEZONE: %w", err))
		} else {
			cfg.Location = loc
		}
	} else if tz := str("TZ", ""); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			cfg.Location = loc
		}
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}

	if !strings.Contains(cfg.Addr, ":") {
		errs = append(errs, fmt.Errorf("HEKATE_ADDR: %q must be host:port", cfg.Addr))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// LogValue renders the config for logging. Secret fields log as [redacted]
// because Secret implements LogValuer; add new fields here as they appear.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("data_dir", c.DataDir),
		slog.String("web_dir", c.WebDir),
		slog.String("assets_dir", c.AssetsDir),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("tamer_name", c.TamerName),
		slog.String("timezone", c.Location.String()),
	)
}
