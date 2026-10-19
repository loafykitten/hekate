package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:3200" || cfg.DataDir != "./data" || cfg.TamerName != "Tamer" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("level = %v", cfg.LogLevel)
	}
	if cfg.WebDir != "" || cfg.AssetsDir != "" {
		t.Errorf("optional dirs should be empty: %+v", cfg)
	}
	if cfg.Location != time.Local {
		t.Errorf("location = %v, want Local", cfg.Location)
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"HEKATE_ADDR":      "0.0.0.0:9000",
		"HEKATE_DATA_DIR":  "/var/lib/h",
		"HEKATE_LOG_LEVEL": "DEBUG",
		"HEKATE_TIMEZONE":  "Europe/Berlin",
		"TZ":               "Asia/Tokyo",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "0.0.0.0:9000" || cfg.DataDir != "/var/lib/h" || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.Location.String() != "Europe/Berlin" {
		t.Errorf("HEKATE_TIMEZONE should beat TZ, got %v", cfg.Location)
	}
}

func TestTZFallback(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{"TZ": "Asia/Tokyo"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Location.String() != "Asia/Tokyo" {
		t.Errorf("location = %v", cfg.Location)
	}
}

func TestInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"bad level", map[string]string{"HEKATE_LOG_LEVEL": "loud"}, "HEKATE_LOG_LEVEL"},
		{"level offset", map[string]string{"HEKATE_LOG_LEVEL": "info+2"}, "HEKATE_LOG_LEVEL"},
		{"bad tz", map[string]string{"HEKATE_TIMEZONE": "Mars/Olympus"}, "HEKATE_TIMEZONE"},
		{"bad addr", map[string]string{"HEKATE_ADDR": "nonsense"}, "HEKATE_ADDR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFrom(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want mention of %s", err, tt.want)
			}
		})
	}
}

func TestSecretNeverLeaks(t *testing.T) {
	s := Secret("hunter2")
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "key", s, "cfg", struct{ K Secret }{s})
	out := buf.String() + fmt.Sprintf("%v %s %#v %+v", s, s, s, struct{ K Secret }{s})
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %s", out)
	}
	if s.Reveal() != "hunter2" {
		t.Fatal("Reveal broken")
	}
}
