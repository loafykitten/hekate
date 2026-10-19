// Package web gives Hekate the open web: Brave search and a safe page reader.
// Everything these tools return is untrusted text from the internet and is
// labelled that way for the model.
package web

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
)

const defaultBraveURL = "https://api.search.brave.com/res/v1/web/search"

// Options are the dependencies of a Service.
type Options struct {
	BraveAPIKey config.Secret // empty: web_search reports itself unavailable
	// BraveURL overrides the search endpoint (tests).
	BraveURL string
	// HTTPClient overrides the client used for search (tests). Page reading
	// always uses its own guarded client.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Service searches and reads the web.
type Service struct {
	key      config.Secret
	braveURL string
	search   *http.Client
	reader   *http.Client // guarded: only connects to addresses guard allows
	guard    guardFunc
	log      *slog.Logger
}

// New builds a Service.
func New(o Options) *Service {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		key:      o.BraveAPIKey,
		braveURL: o.BraveURL,
		search:   o.HTTPClient,
		reader:   newSafeClient(publicOnly),
		guard:    publicOnly,
		log:      log,
	}
	if s.braveURL == "" {
		s.braveURL = defaultBraveURL
	}
	if s.search == nil {
		s.search = &http.Client{Timeout: 10 * time.Second}
	}
	return s
}

// SearchConfigured reports whether web_search can work.
func (s *Service) SearchConfigured() bool { return s.key != "" }

// Register is part of the uniform package shape. The web tools have no REST
// routes in docs/PROTOCOL.md, so it adds nothing.
func (s *Service) Register(*http.ServeMux) {}

// nonce makes the delimiters around untrusted text unguessable, so the text
// cannot fake the end of its own wrapper.
func nonce() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
