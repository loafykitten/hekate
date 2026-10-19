// Package config loads and validates service configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Secret is a string that never renders its value in logs or fmt output.
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

	// Neuralwatt (LLM). An empty key means the agent answers with llm_unavailable.
	NeuralwattAPIKey  Secret
	NeuralwattBaseURL string
	ModelChat         Model // everyday conversation
	ModelDeep         Model // conversation that needs depth (think_deeper)
	ModelBackground   Model // reflection, briefings; not latency sensitive
	ModelBulk         Model // many small calls, e.g. mail classification

	// Voice. Empty ElevenLabs key means text-only replies.
	ElevenLabsAPIKey  Secret
	ElevenLabsVoiceID string
	ElevenLabsModel   string
	STT               bool   // speech-to-text on/off
	ModelsDir         string // downloaded speech models

	// Calendar (CalDAV). An empty URL means the calendar is not configured.
	CalDAVURL      string
	CalDAVUsername string
	CalDAVPassword Secret
	CalDAVCalendar string // display name of the calendar to use; created if missing

	// Web search (Brave). An empty key means web_search is unavailable.
	BraveAPIKey Secret

	Units       string // "imperial" or "metric", for weather
	PushSubject string // VAPID subject: a mailto: or https: URL identifying the sender
}

// Model is a model name plus the reasoning effort every request to it sets.
type Model struct {
	Name   string
	Effort string
}

func (m Model) String() string { return m.Name + "/" + m.Effort }

// efforts are the reasoning levels Neuralwatt accepts.
var efforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

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

	cfg.NeuralwattAPIKey = Secret(str("NEURALWATT_API_KEY", ""))
	cfg.NeuralwattBaseURL = strings.TrimRight(str("NEURALWATT_BASE_URL", "https://api.neuralwatt.com/v1"), "/")
	cfg.ElevenLabsAPIKey = Secret(str("ELEVENLABS_API_KEY", ""))
	cfg.ElevenLabsVoiceID = str("ELEVENLABS_VOICE_ID", "EXAVITQu4vr4xnSDxMaL")
	cfg.ElevenLabsModel = str("ELEVENLABS_MODEL", "eleven_v4")
	cfg.ModelsDir = str("HEKATE_MODELS_DIR", filepath.Join(cfg.DataDir, "models"))
	cfg.CalDAVURL = strings.TrimRight(str("CALDAV_URL", ""), "/") + "/"
	if cfg.CalDAVURL == "/" {
		cfg.CalDAVURL = ""
	}
	cfg.CalDAVUsername = str("CALDAV_USERNAME", "")
	cfg.CalDAVPassword = Secret(str("CALDAV_PASSWORD", ""))
	cfg.CalDAVCalendar = str("CALDAV_CALENDAR", "Calendar")
	cfg.BraveAPIKey = Secret(str("BRAVE_API_KEY", ""))
	cfg.Units = strings.ToLower(str("HEKATE_UNITS", "imperial"))
	cfg.PushSubject = str("HEKATE_PUSH_SUBJECT", "mailto:hekate@example.com")

	var errs []error

	model := func(prefix, defName, defEffort string) Model {
		m := Model{Name: str(prefix, defName), Effort: strings.ToLower(str(prefix+"_EFFORT", defEffort))}
		if !slices.Contains(efforts, m.Effort) {
			errs = append(errs, fmt.Errorf("%s_EFFORT: %q is not one of %s", prefix, m.Effort, strings.Join(efforts, ", ")))
		}
		return m
	}
	cfg.ModelChat = model("HEKATE_MODEL_CHAT", "glm-5.3-flash", "low")
	cfg.ModelDeep = model("HEKATE_MODEL_DEEP", "glm-5.3", "high")
	cfg.ModelBackground = model("HEKATE_MODEL_BACKGROUND", "glm-5.3-flex", "high")
	cfg.ModelBulk = model("HEKATE_MODEL_BULK", "glm-5.3-flash-flex", "low")

	switch v := strings.ToLower(str("HEKATE_STT", "on")); v {
	case "on":
		cfg.STT = true
	case "off":
	default:
		errs = append(errs, fmt.Errorf("HEKATE_STT: %q is not on or off", v))
	}

	if cfg.Units != "imperial" && cfg.Units != "metric" {
		errs = append(errs, fmt.Errorf("HEKATE_UNITS: %q is not imperial or metric", cfg.Units))
	}
	if !strings.HasPrefix(cfg.PushSubject, "mailto:") && !strings.HasPrefix(cfg.PushSubject, "https://") {
		errs = append(errs, fmt.Errorf("HEKATE_PUSH_SUBJECT: %q must start with mailto: or https://", cfg.PushSubject))
	}
	if cfg.CalDAVURL != "" {
		if u, err := url.Parse(cfg.CalDAVURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("CALDAV_URL: %q must be an http(s) URL", cfg.CalDAVURL))
		}
	}

	if u, err := url.Parse(cfg.NeuralwattBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("NEURALWATT_BASE_URL: %q must be an http(s) URL", cfg.NeuralwattBaseURL))
	}

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
		cfg.Location = systemZone()
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
		slog.Bool("llm_configured", c.NeuralwattAPIKey != ""),
		slog.String("neuralwatt_base_url", c.NeuralwattBaseURL),
		slog.String("model_chat", c.ModelChat.String()),
		slog.String("model_deep", c.ModelDeep.String()),
		slog.String("model_background", c.ModelBackground.String()),
		slog.String("model_bulk", c.ModelBulk.String()),
		slog.Bool("tts_configured", c.ElevenLabsAPIKey != ""),
		slog.String("elevenlabs_voice_id", c.ElevenLabsVoiceID),
		slog.String("elevenlabs_model", c.ElevenLabsModel),
		slog.Bool("stt", c.STT),
		slog.String("models_dir", c.ModelsDir),
		slog.String("caldav_url", c.CalDAVURL),
		slog.String("caldav_calendar", c.CalDAVCalendar),
		slog.Bool("web_search_configured", c.BraveAPIKey != ""),
		slog.String("units", c.Units),
		slog.String("push_subject", c.PushSubject),
	)
}

// systemZone names the machine's zone so it can be shared with browsers, which
// reject Go's "Local". It follows /etc/localtime to its zoneinfo name, and
// falls back to UTC.
func systemZone() *time.Location {
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			if loc, err := time.LoadLocation(target[i+len("zoneinfo/"):]); err == nil {
				return loc
			}
		}
	}
	return time.UTC
}
