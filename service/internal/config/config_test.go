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
	if cfg.Location == nil || cfg.Location.String() == "Local" {
		t.Errorf("location = %v, want the system zone by name", cfg.Location)
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

func TestM2Settings(t *testing.T) {
	env := map[string]string{
		"CALDAV_URL": "http://127.0.0.1:5232", "CALDAV_USERNAME": "owner", "CALDAV_PASSWORD": "pw",
		"BRAVE_API_KEY": "k", "HEKATE_UNITS": "metric", "HEKATE_PUSH_SUBJECT": "https://example.com",
	}
	cfg, err := LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CalDAVURL != "http://127.0.0.1:5232/" || cfg.CalDAVCalendar != "Calendar" || cfg.CalDAVPassword.Reveal() != "pw" {
		t.Errorf("caldav = %q %q", cfg.CalDAVURL, cfg.CalDAVCalendar)
	}
	if cfg.Units != "metric" || cfg.PushSubject != "https://example.com" || cfg.BraveAPIKey.Reveal() != "k" {
		t.Errorf("units/push/brave = %q %q", cfg.Units, cfg.PushSubject)
	}

	defaults, err := LoadFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if defaults.CalDAVURL != "" || defaults.Units != "imperial" {
		t.Errorf("defaults: caldav %q units %q", defaults.CalDAVURL, defaults.Units)
	}

	for k, v := range map[string]string{"HEKATE_UNITS": "kelvin", "HEKATE_PUSH_SUBJECT": "me", "CALDAV_URL": "ftp://x"} {
		if _, err := LoadFrom(func(key string) string {
			if key == k {
				return v
			}
			return ""
		}); err == nil {
			t.Errorf("%s=%q: want error", k, v)
		}
	}
}

// With no zone configured, the location must still have a real IANA name:
// browsers reject Go's "Local".
func TestDefaultZoneIsNamed(t *testing.T) {
	cfg, err := LoadFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if name := cfg.Location.String(); name == "Local" || name == "" {
		t.Errorf("default zone = %q", name)
	}
}

func TestMailAccounts(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"HEKATE_MAIL_1_USER": "rika@work.example.com", "HEKATE_MAIL_1_PASS": "pw-one",
		"HEKATE_MAIL_2_USER": "rika@example.org", "HEKATE_MAIL_2_PASS": "pw-two",
		"HEKATE_MAIL_2_NAME": "Home", "HEKATE_MAIL_2_HOST": "imap.example.org:143", "HEKATE_MAIL_2_ARCHIVE": "Old",
		"HEKATE_MAIL_SWEEP": "5m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MailAccounts) != 2 || cfg.MailSweep != 5*time.Minute {
		t.Fatalf("accounts = %+v sweep = %v", cfg.MailAccounts, cfg.MailSweep)
	}
	a, b := cfg.MailAccounts[0], cfg.MailAccounts[1]
	if a.Name != "example" || a.Host != "imappro.zoho.com:993" || a.Archive != "Archive" || a.Pass.Reveal() != "pw-one" {
		t.Errorf("first account = %+v", a)
	}
	if b.Name != "Home" || b.Host != "imap.example.org:143" || b.Archive != "Old" {
		t.Errorf("second account = %+v", b)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "config", cfg)
	if strings.Contains(buf.String(), "pw-one") || strings.Contains(buf.String(), "rika@") {
		t.Errorf("config log leaks mail credentials: %s", buf.String())
	}
}

func TestMailDefaults(t *testing.T) {
	cfg, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MailAccounts) != 0 || cfg.MailSweep != 10*time.Minute {
		t.Errorf("defaults = %+v %v", cfg.MailAccounts, cfg.MailSweep)
	}
}

func TestInvalidMail(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"user without pass", map[string]string{"HEKATE_MAIL_1_USER": "rika@example.com"}, "HEKATE_MAIL_1_USER and HEKATE_MAIL_1_PASS"},
		{"pass without user", map[string]string{"HEKATE_MAIL_3_PASS": "x"}, "HEKATE_MAIL_3_"},
		{"not an address", map[string]string{"HEKATE_MAIL_1_USER": "rika", "HEKATE_MAIL_1_PASS": "x"}, "full mail address"},
		{"duplicate label", map[string]string{
			"HEKATE_MAIL_1_USER": "a@mail.example.com", "HEKATE_MAIL_1_PASS": "x",
			"HEKATE_MAIL_2_USER": "b@example.net", "HEKATE_MAIL_2_PASS": "y"}, "HEKATE_MAIL_2_NAME"},
		{"bad sweep", map[string]string{"HEKATE_MAIL_SWEEP": "soon"}, "HEKATE_MAIL_SWEEP"},
		{"sweep too short", map[string]string{"HEKATE_MAIL_SWEEP": "10s"}, "HEKATE_MAIL_SWEEP"},
		{"bad bulk effort", map[string]string{"HEKATE_MODEL_BULK_EFFORT": "wild"}, "HEKATE_MODEL_BULK_EFFORT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFrom(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want mention of %s", err, tt.want)
			}
			if strings.Contains(err.Error(), "pw") {
				t.Errorf("error leaks a password: %v", err)
			}
		})
	}
}

func TestTelegram(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	cfg, err := LoadFrom(env("HEKATE_TELEGRAM_BOT_TOKEN", "123:abc", "HEKATE_TELEGRAM_CHAT_ID", "8191"))
	if err != nil || cfg.TelegramBotToken.Reveal() != "123:abc" || cfg.TelegramChatID != 8191 {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	if cfg, err := LoadFrom(env()); err != nil || cfg.TelegramBotToken != "" || cfg.TelegramChatID != 0 {
		t.Errorf("unset = %+v, %v", cfg, err)
	}
	for name, e := range map[string]func(string) string{
		"token only": env("HEKATE_TELEGRAM_BOT_TOKEN", "123:abc"),
		"chat only":  env("HEKATE_TELEGRAM_CHAT_ID", "8191"),
		"bad chat":   env("HEKATE_TELEGRAM_BOT_TOKEN", "123:abc", "HEKATE_TELEGRAM_CHAT_ID", "me"),
	} {
		if _, err := LoadFrom(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPlaid(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	cfg, err := LoadFrom(env())
	if err != nil || cfg.PlaidClientID != "" || cfg.PlaidSecret != "" || cfg.PlaidEnv != "production" || cfg.PlaidRedirectURI != "" {
		t.Errorf("unset = %+v, %v", cfg, err)
	}
	cfg, err = LoadFrom(env("PLAID_CLIENT_ID", "id1", "PLAID_SECRET", "s3cret", "PLAID_ENV", "Sandbox",
		"PLAID_REDIRECT_URI", "https://hekate.example.com/money/oauth"))
	if err != nil || cfg.PlaidClientID != "id1" || cfg.PlaidSecret.Reveal() != "s3cret" || cfg.PlaidEnv != "sandbox" ||
		cfg.PlaidRedirectURI != "https://hekate.example.com/money/oauth" {
		t.Errorf("set = %+v, %v", cfg, err)
	}
	for name, e := range map[string]func(string) string{
		"id only":     env("PLAID_CLIENT_ID", "id1"),
		"secret only": env("PLAID_SECRET", "s3cret"),
		"bad env":     env("PLAID_ENV", "development"),
		"http uri":    env("PLAID_REDIRECT_URI", "http://hekate.example.com/money/oauth"),
		"no host uri": env("PLAID_REDIRECT_URI", "https:///money/oauth"),
		"not a uri":   env("PLAID_REDIRECT_URI", "hekate.example.com"),
	} {
		if _, err := LoadFrom(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadFrom(env("PLAID_SECRET", "s3cret")); err != nil && strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the secret: %v", err)
	}
}

func TestLab(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{}))
	if err != nil || cfg.LabHost != "" || cfg.LabName != "hekate-lab" || cfg.LabDir != "/lab" {
		t.Errorf("unset = %+v, %v", cfg, err)
	}
	cfg, err = LoadFrom(env(map[string]string{"HEKATE_LAB_HOST": "100.64.0.9", "HEKATE_LAB_NAME": "lab", "HEKATE_LAB_DIR": "/x"}))
	if err != nil || cfg.LabHost != "100.64.0.9" || cfg.LabName != "lab" || cfg.LabDir != "/x" {
		t.Errorf("set = %+v, %v", cfg, err)
	}
	for _, bad := range []string{"hekate@100.64.0.9", "ssh://lab", "two words"} {
		if _, err := LoadFrom(env(map[string]string{"HEKATE_LAB_HOST": bad})); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}
