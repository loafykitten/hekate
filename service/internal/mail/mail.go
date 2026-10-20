// Package mail is Hekate's view of the owner's mailboxes: IMAP accounts, a
// header cache with her classification, a sweep that classifies and tidies new
// mail, rules, the agent tools and the REST routes the menu uses. The mail
// server stays the source of truth. Message bodies are fetched when asked for
// and are never stored.
//
// Everything from a message (sender, subject, body, even the gist, which is
// derived from them) is untrusted text. It reaches the model only inside
// nonce-delimited wrappers, and nothing in a message can authorize an action:
// approvals and rules carry Hekate message ids and fixed fields, not text from
// mail.
package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/web"
)

// Categories a message can be classified into.
const (
	CategoryNeedsReply   = "needs_reply"
	CategoryImportant    = "important"
	CategoryPersonal     = "personal"
	CategoryReceipt      = "receipt"
	CategoryNotification = "notification"
	CategoryNewsletter   = "newsletter"
	CategoryPromo        = "promo"
)

// categories is the closed set the classifier may answer with, most pressing first.
var categories = []string{
	CategoryNeedsReply, CategoryImportant, CategoryPersonal, CategoryReceipt,
	CategoryNotification, CategoryNewsletter, CategoryPromo,
}

func validCategory(c string) bool {
	for _, v := range categories {
		if v == c {
			return true
		}
	}
	return false
}

// Rule actions.
const (
	ActionArchive     = "archive"
	ActionTrash       = "trash"
	ActionMarkRead    = "mark_read"
	ActionMove        = "move"
	ActionUnsubscribe = "unsubscribe" // one-click (RFC 8058); files nothing, so other rules still apply
)

// Approval kinds this package registers.
const (
	KindTrash       = "mail_trash"
	KindUnsubscribe = "mail_unsubscribe"
	KindBulk        = "mail_bulk"
	KindRuleCreate  = "mail_rule_create"
)

// Limits and timings.
const (
	inbox            = "INBOX"
	maxDirect        = 10                  // messages the model may archive, move or mark without an approval
	maxIDs           = 200                 // ids in one tool call
	firstSyncDays    = 30                  // how far back a first sync (or UIDVALIDITY reset) looks
	firstSyncCap     = 500                 // newest messages kept by a first sync
	classifyDays     = 14                  // older mail is not classified
	classifyBatch    = 20                  // messages per classification request
	classifyPerSweep = 100                 // classification budget per account and sweep
	classifyTries    = 3                   // attempts before a message is left unclassified for good
	snippetRunes     = 1500                // body text shown to the classifier
	maxGist          = 60                  // runes
	maxBodyRunes     = 8000                // body text handed out
	excerptRunes     = 300                 // card excerpt
	retainGone       = 60 * 24 * time.Hour // how long rows for mail that left the inbox are kept
	unsubEvery       = 7 * 24 * time.Hour  // an unsubscribe rule asks a sender's list at most this often
	opTimeout        = 60 * time.Second    // every IMAP operation
	probeTimeout     = 10 * time.Second    // the check on an idle connection
	loginTimeout     = 30 * time.Second
	idleCheck        = 20 * time.Second // a connection idle longer than this is probed before use
	dialTimeout      = 15 * time.Second
	toolTimeout      = 45 * time.Second
	unavailable      = "Mail isn't set up yet."
)

// Errors returned by Service. They carry HTTP statuses, so the same message
// reaches REST clients and the model.
var (
	// ErrNotConfigured means no mail account is set up.
	ErrNotConfigured = &httpx.Error{Status: http.StatusServiceUnavailable, Msg: "mail isn't set up yet"}
	// ErrGone means the message is no longer on the server where Hekate last saw it.
	ErrGone = &httpx.Error{Status: http.StatusNotFound, Msg: "that message is no longer on the mail server"}
	// errNoMove means the server can't move mail atomically, so Hekate won't move it.
	errTimeout = &httpx.Error{Status: http.StatusGatewayTimeout, Msg: "the mail server took too long to answer"}
	errNoMove  = &httpx.Error{Status: http.StatusBadGateway, Msg: "this mail server can't move messages safely (no IMAP MOVE)"}
)

func errNoMessage(id int64) *httpx.Error { return httpx.NotFound("no mail with id %d", id) }

func errServer() *httpx.Error {
	return &httpx.Error{Status: http.StatusBadGateway, Msg: "the mail server could not be reached or refused the request"}
}

// Chat is the part of the LLM client the classifier needs; *llm.Client satisfies it.
type Chat interface {
	Stream(ctx context.Context, req llm.Request) (*llm.Stream, error)
	Configured() bool
}

// Approvals is the part of the approvals service mail needs.
type Approvals interface {
	// Register adds the executor that runs an approved action of this kind.
	Register(kind string, exec approvals.Executor) error
	// Request asks the owner, or runs the action at once under a standing rule.
	Request(ctx context.Context, r approvals.Request) (approvals.Approval, error)
}

// Options are the dependencies of a Service.
type Options struct {
	Store    *store.Store
	Accounts []config.MailAccount // none: mail is unconfigured and every method says so
	// Interval is the time between sweeps. Default 10 minutes.
	Interval time.Duration
	// LLM and Model classify new mail. A nil or unconfigured client skips classification.
	LLM   Chat
	Model config.Model
	// Approvals gates trashing, unsubscribing, bulk changes and new rules. Required when accounts are set.
	Approvals Approvals
	// OwnerName is how tool results name the owner, e.g. "Rika". Default "the owner".
	OwnerName string
	// Location is the zone times are shown to the model in. Default local time.
	Location *time.Location
	Logger   *slog.Logger
	// OnChange is called with "mail" whenever the cache changed, so clients refetch.
	OnChange func(domain string)
	// Now overrides the clock (tests).
	Now func() time.Time
	// HTTPClient overrides the client used for one-click unsubscribe (tests).
	// The default refuses private and internal addresses.
	HTTPClient *http.Client
	// LookupTXT answers the DNS lookups that check DKIM signatures; nil uses
	// net.LookupTXT (tests).
	LookupTXT func(domain string) ([]string, error)
	// Dial overrides how IMAP connections are made (tests). The default dials
	// the account's host with TLS.
	Dial func(ctx context.Context, a config.MailAccount) (net.Conn, error)
}

// Service owns the mail accounts, the cache and the sweep.
type Service struct {
	db        *sql.DB
	store     *store.Store
	accounts  []*account
	byName    map[string]*account
	llm       Chat
	model     config.Model
	appr      Approvals
	owner     string
	loc       *time.Location
	interval  time.Duration
	log       *slog.Logger
	onChange  func(string)
	now       func() time.Time
	http      *http.Client
	dial      func(ctx context.Context, a config.MailAccount) (net.Conn, error)
	lookupTXT func(domain string) ([]string, error)
	// opTimeout bounds each IMAP call; tests shorten it.
	opTimeout time.Duration
}

// New builds a Service. Call Run to start the sweep. It registers the approval
// executors, so call it once.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("mail: Store is required")
	}
	if len(o.Accounts) > 0 && o.Approvals == nil {
		return nil, errors.New("mail: Approvals is required when accounts are configured")
	}
	s := &Service{
		db: o.Store.DB(), store: o.Store, llm: o.LLM, model: o.Model, appr: o.Approvals,
		owner: o.OwnerName, loc: o.Location, interval: o.Interval, log: o.Logger, onChange: o.OnChange,
		now: o.Now, http: o.HTTPClient, dial: o.Dial, lookupTXT: o.LookupTXT,
		byName: map[string]*account{}, opTimeout: opTimeout,
	}
	if s.owner == "" {
		s.owner = "the owner"
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.interval <= 0 {
		s.interval = 10 * time.Minute
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.http == nil {
		s.http = web.NewSafeClient(nil)
	}
	s.http = noRedirects(s.http)
	if s.dial == nil {
		s.dial = dialTLS
	}
	for _, cfg := range o.Accounts {
		if _, dup := s.byName[cfg.Name]; dup {
			return nil, fmt.Errorf("mail: two accounts are named %q", cfg.Name)
		}
		a := &account{cfg: cfg}
		s.accounts = append(s.accounts, a)
		s.byName[cfg.Name] = a
	}
	if err := s.bindAccounts(context.Background()); err != nil {
		return nil, err
	}
	if s.appr != nil {
		for kind, exec := range map[string]approvals.Executor{
			KindTrash: s.execTrash, KindUnsubscribe: s.execUnsubscribe, KindBulk: s.execBulk, KindRuleCreate: s.execRuleCreate,
		} {
			if err := s.appr.Register(kind, exec); err != nil {
				return nil, fmt.Errorf("mail: %w", err)
			}
		}
	}
	return s, nil
}

// noRedirects makes c report a redirect instead of following it: following a
// POST redirect turns it into a GET of a link, which unsubscribing must never do.
func noRedirects(c *http.Client) *http.Client {
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// Configured reports whether any mail account is set up.
func (s *Service) Configured() bool { return len(s.accounts) > 0 }

func (s *Service) changed() {
	if s.onChange != nil {
		s.onChange("mail")
	}
}

// stamp formats t for storage: UTC RFC 3339.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// parseID reads a Hekate message id from a REST path or a string.
func parseID(v string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("message id must be a positive whole number")
	}
	return id, nil
}

// oneLine flattens text from a message to a single short line.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// clipRunes cuts s to at most n runes on a word boundary where it can.
func clipRunes(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \n"); i > n*8/10 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…", true
}
