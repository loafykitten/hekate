// Package server builds the HTTP handler: health, character assets, the API
// namespace and the static PWA.
package server

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/loafykitten/hekate/service/internal/buildinfo"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
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
	// Threads, if set, takes socket input sent to a thread.
	Threads Threads
	Hub     *Hub
	// STT may be nil (not configured); TTS says whether replies carry audio.
	STT voice.Transcriber
	TTS bool

	// API registers REST routes of feature packages (tasks, reminders, ...).
	API []func(*http.ServeMux)

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
	api       []func(*http.ServeMux)
	stt       voice.Transcriber
	tts       bool
	pingEvery time.Duration
	pingWait  time.Duration

	threads Threads

	// inputs counts new inputs (typed or push-to-talk) per conversation, across
	// every connection, since all of them talk to the same conversations. A
	// transcription that finishes after newer input from any device is stale.
	// inputMu also makes "is it still current?" and starting the turn one step.
	inputMu sync.Mutex
	inputs  map[string]uint64
}

// Input is what socket input goes to: the main conversation or a thread.
type Input interface {
	ConversationID() string
	Say(origin, inputID, text string, quiet bool)
	Interrupt(turnID string, heardSeq int)
	// Hush stops whatever she's saying because someone started talking.
	Hush()
}

// Threads finds a thread for socket input.
type Threads interface {
	// Input returns the thread's conversation, or an error for an unknown or
	// archived thread.
	Input(ctx context.Context, id string) (Input, error)
}

// startPTT records a push-to-talk as in's newest input and hushes her there,
// as one step: no other input can be ordered between them (or an older
// recording could cut a newer typed turn). Returns the input's number.
func (s *Server) startPTT(in Input) uint64 {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.inputs[in.ConversationID()]++
	if testHookInPTTStart != nil {
		testHookInPTTStart()
	}
	in.Hush()
	return s.inputs[in.ConversationID()]
}

// testHookInPTTStart lets tests try to slip an input into startPTT.
var testHookInPTTStart func()

// sayIfCurrent starts a turn only if no newer input arrived since gen, and
// reports whether it did. before runs first under the same lock (to send the
// transcript ahead of the turn).
func (s *Server) sayIfCurrent(in Input, gen uint64, before func(), origin, id, text string, quiet bool) bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.inputs[in.ConversationID()] != gen {
		return false
	}
	before()
	if text != "" {
		in.Say(origin, id, text, quiet)
	}
	return true
}

// say records a typed input and starts its turn.
func (s *Server) say(in Input, origin, id, text string, quiet bool) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.inputs[in.ConversationID()]++
	in.Say(origin, id, text, quiet)
}

// checkIn starts the evening check-in: input like any other.
func (s *Server) checkIn(origin, id, day string, quiet bool) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.inputs[s.conv.ConversationID()]++
	s.conv.CheckIn(origin, id, day, quiet)
}

// New creates a Server. Directories are opened lazily per request through
// os.Root so a missing or replaced directory never crashes the process.
func New(o Options) *Server {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		log:       log,
		db:        o.DB,
		webDir:    o.WebDir,
		assetsDir: o.AssetsDir,
		startedAt: time.Now().UTC(),
		conv:      o.Conversation,
		threads:   o.Threads,
		inputs:    map[string]uint64{},
		hub:       o.Hub,
		api:       o.API,
		stt:       o.STT,
		tts:       o.TTS,
		pingEvery: cmp.Or(o.PingInterval, pingInterval),
		pingWait:  cmp.Or(o.PingTimeout, pingTimeout),
	}
	// Speech-to-text may still be loading when clients connect (they get
	// stt:false in ready); tell them when it's done.
	if loading, ok := o.STT.(interface{ Done() <-chan struct{} }); ok && s.hub != nil {
		go func() {
			<-loading.Done()
			s.hub.Broadcast(proto.NewCapabilities(s.capabilities()))
		}()
	}
	return s
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

	// State-changing requests must come from this origin. Without this, a page
	// on another site, opened on one of the owner's (allowlisted) devices, could
	// fire simple cross-origin POSTs at the API: the reverse proxy would see an
	// allowed device. Uses Sec-Fetch-Site, falling back to Origin.
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request refused"})
	}))

	return chain(csrf.Handler(mux),
		s.requestID,
		s.accessLog,
		s.recoverer,
		secureHeaders,
	)
}

// apiRoutes is where feature milestones register /api/... endpoints.
func (s *Server) apiRoutes(mux *http.ServeMux) {
	s.conversationRoutes(mux)
	for _, register := range s.api {
		register(mux)
	}
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

func writeJSON(w http.ResponseWriter, status int, v any) { httpx.JSON(w, status, v) }

// reservedPrefix reports paths the SPA fallback must never claim.
func reservedPrefix(p string) bool {
	if p == "/healthz" || p == "/api" || p == "/character" {
		return true
	}
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/character/")
}
