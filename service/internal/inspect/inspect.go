// Package inspect lets Renamon look at herself: what's configured and healthy,
// recent warnings in her logs, what her thinking costs, what her tools and
// routines did, and how deploys went. It also keeps her improvement ideas.
package inspect

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/logging"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

// DomainIdeas is the invalidate domain of the ideas list.
const DomainIdeas = "ideas"

// Check reports on one part of Hekate: whether it's working, and a few words on it.
type Check struct {
	Name string
	Run  func(ctx context.Context) (ok bool, detail string)
}

// Options are the dependencies of a Service.
type Options struct {
	Store     *store.Store
	Jobs      *jobs.Runner   // nil: no routines
	Ring      *logging.Ring  // nil: no logs to read
	DeployLog string         // the deployer's JSON-lines log; empty when not mounted
	Checks    []Check        // what self_status reports on, in order
	Version   string         // shown by self_status
	Started   time.Time      // when the service started
	Location  *time.Location // times are shown in the owner's zone
	Events    proto.Events
	Logger    *slog.Logger
	Now       func() time.Time // tests replace the clock
}

// Service answers the self_* tools and keeps ideas.
type Service struct {
	db        *sql.DB
	jobs      *jobs.Runner
	ring      *logging.Ring
	deployLog string
	checks    []Check
	version   string
	started   time.Time
	loc       *time.Location
	events    proto.Events
	log       *slog.Logger
	now       func() time.Time
}

// New builds the service.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("inspect: Store is required")
	}
	s := &Service{db: o.Store.DB(), jobs: o.Jobs, ring: o.Ring, deployLog: o.DeployLog, checks: o.Checks,
		version: o.Version, started: o.Started, loc: o.Location, events: o.Events, log: o.Logger, now: o.Now}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.events == nil {
		s.events = proto.NopEvents{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.started.IsZero() {
		s.started = s.now()
	}
	return s, nil
}

// local formats a stored or live instant in the owner's zone, to the minute.
func (s *Service) local(t time.Time) string { return t.In(s.loc).Format("Mon Jan 2 15:04") }

// localStamp parses a stored RFC 3339 time and formats it like local; it
// returns the input when it doesn't parse.
func (s *Service) localStamp(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return s.local(t)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
