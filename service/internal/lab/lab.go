// Package lab is her own machine: a Debian VM on the homelab (deploy/lab) that she runs commands
// on over SSH, root inside it and kept to the internet by the host's firewall. Hekate's own data
// and secrets never go there. The host does what needs the hypervisor (snapshots, rolling back,
// cutting the network) through hekate-labctl, asked by request files. A daily security check
// compares the lab with published vulnerabilities and looks for signs of a break-in, and cuts the
// lab off by itself when it finds strong ones.
package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

// Approval kinds.
const (
	KindRollback  = "lab_rollback"
	KindReconnect = "lab_reconnect"
)

// User is the account she has on the lab, with passwordless sudo.
const User = "hekate"

// Pusher delivers web push; push.Service satisfies it.
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Options are the dependencies of a Service.
type Options struct {
	Store     *store.Store
	Approvals *approvals.Service
	// Host is the VM's SSH address (host or host:port); empty means the lab isn't set up.
	Host string
	// Name is what the owner's devices call the lab, for the addresses of apps there.
	Name string
	// Dir holds ssh/ (id_ed25519, host_key.pub), requests/ and results/ (deploy/lab/README.md).
	Dir string
	// CacheDir keeps the known-exploited list between checks.
	CacheDir string
	Location *time.Location
	Events   proto.Events
	Push     Pusher // nil: in-app notifications only
	Logger   *slog.Logger
	// LLM and Model run lab tasks' workers; nil LLM means no lab tasks.
	LLM   Completer
	Model config.Model
	// HTTP fetches the known-exploited list; nil uses a client with a timeout.
	HTTP *http.Client
	Now  func() time.Time // tests replace the clock
	// Dial opens the TCP connection to the lab; tests replace it.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
}

// Service runs commands on the lab, asks the host for what needs the hypervisor, and checks
// the lab's security.
type Service struct {
	store    *store.Store
	db       *sql.DB
	appr     *approvals.Service
	addr     string
	name     string
	dir      string
	cacheDir string
	loc      *time.Location
	events   proto.Events
	pusher   Pusher
	log      *slog.Logger
	http     *http.Client
	now      func() time.Time
	dial     func(ctx context.Context, addr string) (net.Conn, error)
	ctl      *ctl
	kevURL   string // tests point it elsewhere; empty is CISA's feed

	llm   Completer
	model config.Model

	checking sync.Mutex // one security check at a time

	bgMu  sync.Mutex
	bgCtx context.Context //nolint:containedctx // Run's context, for background work started by tools
	bg    sync.WaitGroup

	taskMu        sync.Mutex
	taskBusy      bool
	taskCancel    context.CancelFunc
	stopRequested atomic.Bool
}

// New builds the service and registers its approval kinds. With no host it's built anyway and
// reports that the lab isn't set up.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Approvals == nil {
		return nil, errors.New("lab: Store and Approvals are required")
	}
	s := &Service{store: o.Store, db: o.Store.DB(), llm: o.LLM, model: o.Model, appr: o.Approvals, name: o.Name, dir: o.Dir, cacheDir: o.CacheDir, loc: o.Location,
		events: o.Events, pusher: o.Push, log: o.Logger, http: o.HTTP, now: o.Now, dial: o.Dial}
	if o.Host != "" {
		s.addr = o.Host
		if _, _, err := net.SplitHostPort(o.Host); err != nil {
			s.addr = net.JoinHostPort(o.Host, "22")
		}
	}
	if s.name == "" {
		s.name = "hekate-lab"
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.events == nil {
		s.events = proto.NopEvents{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: 60 * time.Second}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.dial == nil {
		var d net.Dialer
		s.dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	s.ctl = &ctl{dir: s.dir, now: s.now}
	if err := o.Approvals.Register(KindRollback, s.rollback); err != nil {
		return nil, fmt.Errorf("lab: %w", err)
	}
	if err := o.Approvals.Register(KindReconnect, s.reconnect); err != nil {
		return nil, fmt.Errorf("lab: %w", err)
	}
	return s, nil
}

// Configured reports whether the lab is set up.
func (s *Service) Configured() bool { return s.addr != "" }

// errNotSetUp is what her lab tools say when there's no lab.
var errNotSetUp = errors.New("the lab isn't set up yet: the owner builds it on the homelab (deploy/lab in the Hekate repo)")

func (s *Service) keyPath() string     { return filepath.Join(s.dir, "ssh", "id_ed25519") }
func (s *Service) hostKeyPath() string { return filepath.Join(s.dir, "ssh", "host_key.pub") }

// readFile reads a small file the stack mounts in.
func readFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Health says whether she can log in to the lab, and the last security check's verdict.
func (s *Service) Health(ctx context.Context) (bool, string) {
	res, err := s.shell(ctx, "true", nil, 5*time.Second, 1024)
	if err != nil {
		return false, err.Error()
	}
	if res.Exit != 0 {
		return false, fmt.Sprintf("logging in works, but a test command failed (exit %d)", res.Exit)
	}
	c, err := s.lastCheck(ctx)
	switch {
	case err != nil:
		return false, err.Error()
	case c == nil:
		return true, "reachable; no security check yet"
	case rank[c.State] >= rank[StateSuspicious]:
		return false, "last security check: " + c.State
	}
	return true, "reachable; last security check: " + c.State
}
