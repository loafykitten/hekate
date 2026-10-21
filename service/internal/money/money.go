// Package money is Hekate's read-only view of the owner's bank accounts through
// Plaid: connected banks (items), their accounts and balances, and a local copy
// of the transactions, kept fresh by an hourly sync. Only the Transactions
// product is ever enabled, so nothing here can move money.
//
// An item's access token stays in the database. It is never returned by REST,
// shown to the model or logged, and neither are transactions: logs carry item
// ids and Plaid error codes only. What the bank writes (transaction names,
// merchants, account names) is untrusted text and reaches the model only inside
// nonce-delimited wrappers.
//
// Plaid reports money out as a positive amount. Everything outside this package
// uses money in (+) and out (-); the sign is flipped once, when a transaction is
// stored (see cents).
package money

import (
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

// JobName is the sync routine's name in job_runs and self_status.
const JobName = "money"

// Item statuses.
const (
	StatusOK            = "ok"
	StatusLoginRequired = "login_required"
	StatusError         = "error"
)

// Limits and timings.
const (
	syncEvery      = time.Hour
	syncTimeout    = 10 * time.Minute // one sync of every item
	requestTimeout = 60 * time.Second // one call to Plaid
	toolTimeout    = 30 * time.Second
	defaultList    = 100 // REST transactions
	maxList        = 500
	defaultSearch  = 20 // tool transactions
	maxSearch      = 50
	maxCardItems   = 20
	unavailable    = "Bank data isn't set up yet."
	noBanks        = "No bank is connected yet. The owner can connect one from the app."
)

// ErrNotConfigured means no Plaid keys are set.
var ErrNotConfigured = &httpx.Error{Status: http.StatusServiceUnavailable, Msg: "bank data isn't set up yet"}

func errNoItem(id string) *httpx.Error { return httpx.NotFound("no bank with id %s", id) }

func errPlaid() *httpx.Error {
	return &httpx.Error{Status: http.StatusBadGateway, Msg: "the bank data service refused the request or could not be reached"}
}

// Options are the dependencies of a Service.
type Options struct {
	Store *store.Store
	// ClientID and Secret are Plaid's keys; leave both empty and money is unconfigured.
	ClientID string
	Secret   string
	// Env is "production" (default) or "sandbox".
	Env string
	// RedirectURI is where banks that link through their own site send the owner back.
	RedirectURI string
	// BaseURL overrides Plaid's address (tests).
	BaseURL string
	// HTTPClient overrides the HTTP client (tests).
	HTTPClient *http.Client
	// Location is the zone calendar months are cut in. Default local time.
	Location *time.Location
	Events   proto.Events
	Logger   *slog.Logger
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Service owns the connected banks and the local copy of their data.
type Service struct {
	db          *sql.DB
	plaid       *client // nil: unconfigured
	redirectURI string
	loc         *time.Location
	events      proto.Events
	log         *slog.Logger
	now         func() time.Time

	syncMu sync.Mutex     // one sync at a time
	wg     sync.WaitGroup // background syncs, so tests can wait for them
	// Extra syncs after a bank is connected: Plaid gathers its history over the
	// first minutes, so the first sync usually finds little. Tests clear it.
	afterConnect []time.Duration
}

// New builds a Service. With no keys it still works: every method says money isn't set up.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("money: Store is required")
	}
	if (o.ClientID == "") != (o.Secret == "") {
		return nil, errors.New("money: ClientID and Secret go together")
	}
	s := &Service{
		db: o.Store.DB(), redirectURI: o.RedirectURI, loc: o.Location, events: o.Events, log: o.Logger, now: o.Now,
		afterConnect: []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute},
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
	if s.now == nil {
		s.now = time.Now
	}
	if o.ClientID != "" {
		base := o.BaseURL
		if base == "" {
			base = productionURL
			if o.Env == "sandbox" {
				base = sandboxURL
			}
		}
		hc := o.HTTPClient
		if hc == nil {
			hc = &http.Client{Timeout: requestTimeout}
		}
		s.plaid = &client{http: hc, base: strings.TrimRight(base, "/"), clientID: o.ClientID, secret: o.Secret}
	}
	return s, nil
}

// Configured reports whether Plaid keys are set.
func (s *Service) Configured() bool { return s.plaid != nil }

func (s *Service) changed() { s.events.Invalidate(proto.DomainMoney) }

// stamp formats t for storage: UTC RFC 3339.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// local renders a stored timestamp in the owner's zone; "" stays nil.
func (s *Service) local(v sql.NullString) *string {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v.String)
	if err != nil {
		return &v.String
	}
	out := t.In(s.loc).Format(time.RFC3339)
	return &out
}

// cents converts a Plaid amount (positive = money out) to cents with money in
// positive and out negative. This is the one place the sign flips.
func cents(plaidAmount float64) int64 { return -int64(math.Round(plaidAmount * 100)) }

// balanceCents converts a balance, which keeps Plaid's sign, to cents.
func balanceCents(v *float64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(math.Round(*v * 100)), Valid: true}
}

// dollars converts cents to the amount the API shows.
func dollars(c int64) float64 { return float64(c) / 100 }

func dollarsPtr(v sql.NullInt64) *float64 {
	if !v.Valid {
		return nil
	}
	d := dollars(v.Int64)
	return &d
}

func parseItemID(v string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("bank id must be a positive whole number")
	}
	return id, nil
}

// oneLine flattens text from the bank to a single short line.
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
