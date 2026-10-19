// Package server builds the HTTP handler: health, character assets, the API
// namespace and the static PWA.
package server

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/buildinfo"
	"github.com/loafykitten/hekate/service/internal/voice"
)

// Pinger is the slice of the store the server needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Options are the dependencies injected into a Server. Later milestones add
// fields (agent, hub, ...) here rather than reaching for globals.
type Options struct {
	Logger    *slog.Logger
	DB        Pinger
	WebDir    string // optional: built PWA
	AssetsDir string // optional: character assets

	// Conversation and Hub enable /api/ws and the history endpoint.
	Conversation Conversation
	Hub          *Hub
	// STT may be nil (not configured); TTS says whether replies carry audio.
	STT voice.Transcriber
	TTS bool

	// PingInterval and PingTimeout default to 20 s and 10 s; tests shorten them.
	PingInterval time.Duration
	PingTimeout  time.Duration
}

// Server owns routing and its dependencies.
type Server struct {
	log       *slog.Logger
	db        Pinger
	webDir    string
	assetsDir string
	startedAt time.Time
	conv      Conversation
	hub       *Hub
	stt       voice.Transcriber
	tts       bool
	pingEvery time.Duration
	pingWait  time.Duration
}

// New creates a Server. Directories are opened lazily per request through
// os.Root so a missing or replaced directory never crashes the process.
func New(o Options) *Server {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		log:       log,
		db:        o.DB,
		webDir:    o.WebDir,
		assetsDir: o.AssetsDir,
		startedAt: time.Now().UTC(),
		conv:      o.Conversation,
		hub:       o.Hub,
		stt:       o.STT,
		tts:       o.TTS,
		pingEvery: cmp.Or(o.PingInterval, pingInterval),
		pingWait:  cmp.Or(o.PingTimeout, pingTimeout),
	}
}

// Routes returns the fully wrapped handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)

	if s.assetsDir != "" {
		mux.HandleFunc("GET /character/{file}", s.handleCharacter)
	}

	s.apiRoutes(mux)
	// Anything under /api/ that no route claimed is a JSON 404, never the SPA.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})

	// All methods: "GET /" would conflict with the method-less /api/ catch-all.
	mux.HandleFunc("/", s.handleStatic)

	return chain(mux,
		s.requestID,
		s.accessLog,
		s.recoverer,
		secureHeaders,
	)
}

// apiRoutes is where feature milestones register /api/... endpoints.
func (s *Server) apiRoutes(mux *http.ServeMux) {
	s.conversationRoutes(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	db := "ok"
	var err error
	if s.db == nil {
		db = "not configured"
		err = os.ErrInvalid
	} else {
		err = s.db.Ping(ctx)
		if err != nil {
			db = err.Error()
		}
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"ok":         err == nil,
		"version":    buildinfo.Version,
		"commit":     buildinfo.Commit,
		"built_at":   buildinfo.BuiltAt,
		"started_at": s.startedAt.Format(time.RFC3339),
		"uptime_s":   int64(time.Since(s.startedAt).Seconds()),
		"db":         db,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// reservedPrefix reports paths the SPA fallback must never claim.
func reservedPrefix(p string) bool {
	if p == "/healthz" || p == "/api" || p == "/character" {
		return true
	}
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/character/")
}
