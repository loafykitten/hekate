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

func TestModelAndVoiceDefaults(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{"HEKATE_DATA_DIR": "/d"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Model{
		"chat":       {"glm-5.3-flash", "low"},
		"deep":       {"glm-5.3", "high"},
		"background": {"glm-5.3-flex", "high"},
		"bulk":       {"glm-5.3-flash-flex", "low"},
	}
	got := map[string]Model{"chat": cfg.ModelChat, "deep": cfg.ModelDeep, "background": cfg.ModelBackground, "bulk": cfg.ModelBulk}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("model %s = %+v, want %+v", k, got[k], w)
		}
	}
	if cfg.NeuralwattBaseURL != "https://api.neuralwatt.com/v1" || cfg.NeuralwattAPIKey != "" {
		t.Errorf("neuralwatt = %q %q", cfg.NeuralwattBaseURL, cfg.NeuralwattAPIKey)
	}
	if cfg.ElevenLabsVoiceID != "EXAVITQu4vr4xnSDxMaL" || cfg.ElevenLabsModel != "eleven_v4" || cfg.ElevenLabsAPIKey != "" {
		t.Errorf("elevenlabs = %+v", cfg)
	}
	if !cfg.STT || cfg.ModelsDir != "/d/models" {
		t.Errorf("stt=%v models=%q", cfg.STT, cfg.ModelsDir)
	}
}

func TestModelOverrides(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"HEKATE_MODEL_CHAT":        "m1",
		"HEKATE_MODEL_CHAT_EFFORT": "MEDIUM",
		"HEKATE_MODEL_DEEP_EFFORT": "max",
		"HEKATE_STT":               "off",
		"HEKATE_MODELS_DIR":        "/m",
		"NEURALWATT_BASE_URL":      "http://localhost:1/v1/",
		"ELEVENLABS_VOICE_ID":      "v",
		"ELEVENLABS_MODEL":         "eleven_flash_v2_5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelChat != (Model{"m1", "medium"}) || cfg.ModelDeep != (Model{"glm-5.3", "max"}) {
		t.Errorf("models = %+v %+v", cfg.ModelChat, cfg.ModelDeep)
	}
	if cfg.STT || cfg.ModelsDir != "/m" || cfg.NeuralwattBaseURL != "http://localhost:1/v1" ||
		cfg.ElevenLabsVoiceID != "v" || cfg.ElevenLabsModel != "eleven_flash_v2_5" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestInvalidModelAndVoice(t *testing.T) {
	for name, tt := range map[string]struct{ key, val string }{
		"effort":  {"HEKATE_MODEL_BULK_EFFORT", "turbo"},
		"stt":     {"HEKATE_STT", "maybe"},
		"baseurl": {"NEURALWATT_BASE_URL", "ftp://x"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFrom(env(map[string]string{tt.key: tt.val}))
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("err = %v, want mention of %s", err, tt.key)
			}
		})
	}
}

func TestLogValueHidesKeys(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{"NEURALWATT_API_KEY": "nw-secret", "ELEVENLABS_API_KEY": "el-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "config", cfg)
	out := buf.String() + fmt.Sprintf("%v %+v", cfg, cfg)
	if strings.Contains(out, "nw-secret") || strings.Contains(out, "el-secret") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(buf.String(), `"llm_configured":true`) || !strings.Contains(buf.String(), `"tts_configured":true`) {
		t.Errorf("configured flags missing: %s", buf.String())
	}
}
