// Package push sends web push notifications to the owner's browsers. VAPID
// keys are generated once and kept in the settings table.
package push

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/store"
)

// Settings keys for the VAPID key pair.
const (
	settingPrivate = "push.vapid_private"
	settingPublic  = "push.vapid_public"
)

// Delivery bounds.
const (
	// ttl is how long a push service holds a message for an offline device.
	ttl             = 24 * 60 * 60 // seconds
	sendTimeout     = 10 * time.Second
	maxParallel     = 6
	maxSubs         = 20
	maxEndpointLen  = 2048
	p256dhKeyLength = 65 // an uncompressed P-256 point
)

// Urgency of a message; the push service uses it to decide how eagerly to wake the device.
type Urgency = webpush.Urgency

// Urgencies.
const (
	UrgencyNormal = webpush.UrgencyNormal
	UrgencyHigh   = webpush.UrgencyHigh
)

// Payload is what the service worker receives (PROTOCOL.md).
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// Subscription is a browser's PushSubscription.toJSON().
type Subscription struct {
	Endpoint       string `json:"endpoint"`
	ExpirationTime any    `json:"expirationTime,omitempty"`
	Keys           struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Result says how a send went.
type Result struct {
	Sent    int // accepted by the push service
	Removed int // subscriptions the push service said are gone (404/410), now deleted
	Failed  int // other errors; the subscription is kept
}

// Options are the dependencies of a Service.
type Options struct {
	Store   *store.Store
	Subject string // VAPID subject: mailto: or https: URL
	Logger  *slog.Logger
	Client  webpush.HTTPClient // nil: a client with a timeout
	// AllowInsecureEndpoints accepts http endpoints. Only tests need it: real
	// push services are https.
	AllowInsecureEndpoints bool
	Now                    func() time.Time
}

// Service stores subscriptions and sends notifications.
type Service struct {
	store    *store.Store
	db       *sql.DB
	subject  string
	log      *slog.Logger
	client   webpush.HTTPClient
	insecure bool
	now      func() time.Time

	private, public string
}

// New loads the VAPID keys, generating and storing them on first use.
func New(ctx context.Context, o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("push: Store is required")
	}
	s := &Service{store: o.Store, db: o.Store.DB(), subject: o.Subject, log: o.Logger, client: o.Client,
		insecure: o.AllowInsecureEndpoints, now: o.Now}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: sendTimeout + 5*time.Second}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.subject == "" {
		s.subject = "mailto:hekate@example.com"
	}
	if err := s.loadKeys(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) loadKeys(ctx context.Context) error {
	priv, errPriv := s.store.GetSetting(ctx, settingPrivate)
	pub, errPub := s.store.GetSetting(ctx, settingPublic)
	for _, err := range []error{errPriv, errPub} {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("load VAPID keys: %w", err)
		}
	}
	if errPriv == nil && errPub == nil && priv != "" && pub != "" {
		s.private, s.public = priv, pub
		return nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("generate VAPID keys: %w", err)
	}
	// Subscriptions made against an older key would be rejected, so a lone
	// half-stored key is replaced as a pair.
	if err := s.store.SetSetting(ctx, settingPrivate, priv); err != nil {
		return fmt.Errorf("store VAPID private key: %w", err)
	}
	if err := s.store.SetSetting(ctx, settingPublic, pub); err != nil {
		return fmt.Errorf("store VAPID public key: %w", err)
	}
	s.private, s.public = priv, pub
	s.log.Info("generated VAPID keys")
	return nil
}

// PublicKey is the application server key browsers subscribe with (base64url).
func (s *Service) PublicKey() string { return s.public }

// Subscribe stores or refreshes a subscription.
func (s *Service) Subscribe(ctx context.Context, sub Subscription) error {
	if err := s.validate(sub); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions WHERE endpoint <> ?`, sub.Endpoint).Scan(&n); err != nil {
		return fmt.Errorf("count subscriptions: %w", err)
	}
	if n >= maxSubs {
		return httpx.Conflict("too many devices are subscribed; remove one first")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions (endpoint, p256dh, auth, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth`,
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, s.now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("store subscription: %w", err)
	}
	return nil
}

func (s *Service) validate(sub Subscription) error {
	u, err := url.Parse(sub.Endpoint)
	switch {
	case sub.Endpoint == "" || len(sub.Endpoint) > maxEndpointLen || err != nil || u.Host == "":
		return httpx.BadRequest("endpoint must be the push service URL")
	case u.Scheme != "https" && (!s.insecure || u.Scheme != "http"):
		return httpx.BadRequest("endpoint must be an https URL")
	}
	if k, err := decodeKey(sub.Keys.P256dh); err != nil || len(k) != p256dhKeyLength {
		return httpx.BadRequest("keys.p256dh must be a base64url P-256 public key")
	}
	if k, err := decodeKey(sub.Keys.Auth); err != nil || len(k) == 0 {
		return httpx.BadRequest("keys.auth must be base64url")
	}
	return nil
}

// decodeKey reads base64url with or without padding, as browsers and libraries differ.
func decodeKey(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// Unsubscribe removes a subscription; an unknown endpoint is not an error.
func (s *Service) Unsubscribe(ctx context.Context, endpoint string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint); err != nil {
		return fmt.Errorf("remove subscription: %w", err)
	}
	return nil
}

// Count returns how many devices are subscribed.
func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count subscriptions: %w", err)
	}
	return n, nil
}

func (s *Service) subscriptions(ctx context.Context) ([]webpush.Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT endpoint, p256dh, auth FROM push_subscriptions ORDER BY created_at, endpoint`)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []webpush.Subscription
	for rows.Next() {
		var sub webpush.Subscription
		if err := rows.Scan(&sub.Endpoint, &sub.Keys.P256dh, &sub.Keys.Auth); err != nil {
			return nil, fmt.Errorf("list subscriptions: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err() //nolint:wrapcheck // wrapped by the caller's context
}

// Send delivers p to every subscribed device. A subscription the push service
// reports gone is deleted. Failures are logged and counted, never fatal: one
// dead phone must not stop the others.
func (s *Service) Send(ctx context.Context, p Payload, urgency Urgency) (Result, error) {
	subs, err := s.subscriptions(ctx)
	if err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return Result{}, fmt.Errorf("encode push payload: %w", err)
	}
	var (
		mu  sync.Mutex
		res Result
		sem = make(chan struct{}, maxParallel)
		wg  sync.WaitGroup
	)
	for _, sub := range subs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			// webpush-go pads the message in place, so each send gets its own copy.
			outcome := s.sendOne(ctx, slices.Clone(body), sub, urgency)
			mu.Lock()
			defer mu.Unlock()
			switch outcome {
			case sent:
				res.Sent++
			case gone:
				res.Removed++
			default:
				res.Failed++
			}
		})
	}
	wg.Wait()
	return res, nil
}

type outcome int

const (
	sent outcome = iota
	gone
	failed
)

func (s *Service) sendOne(ctx context.Context, body []byte, sub webpush.Subscription, urgency Urgency) outcome {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	host := endpointHost(sub.Endpoint) // the path is a capability; keep it out of logs
	resp, err := webpush.SendNotificationWithContext(ctx, body, &sub, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      subscriber(s.subject),
		TTL:             ttl,
		Urgency:         urgency,
		VAPIDPublicKey:  s.public,
		VAPIDPrivateKey: s.private,
	})
	if err != nil {
		s.log.Warn("push send failed", "host", host, "error", err)
		return failed
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// The browser unsubscribed or the subscription expired.
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer dcancel()
		if err := s.Unsubscribe(dctx, sub.Endpoint); err != nil {
			s.log.Error("cannot remove dead push subscription", "host", host, "error", err)
		} else {
			s.log.Info("removed dead push subscription", "host", host, "status", resp.StatusCode)
		}
		return gone
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return sent
	default:
		s.log.Warn("push service rejected the message", "host", host, "status", resp.StatusCode)
		return failed
	}
}

func endpointHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Host
	}
	return "unknown"
}

// subscriber adapts our VAPID subject to webpush-go, which adds "mailto:" to any
// subject that isn't an https URL; passing ours unchanged would produce
// "mailto:mailto:…", which push services may reject.
func subscriber(subject string) string {
	return strings.TrimPrefix(subject, "mailto:")
}
