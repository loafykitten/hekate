package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/loafykitten/hekate/service/internal/store"
)

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	db, err := store.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// browserKeys makes the key pair a real browser would hand over.
func browserKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	if _, err := rand.Read(a); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(a)
}

func subscription(t *testing.T, endpoint string) Subscription {
	t.Helper()
	var sub Subscription
	sub.Endpoint = endpoint
	sub.Keys.P256dh, sub.Keys.Auth = browserKeys(t)
	return sub
}

// pushEndpoint is a fake push service recording what it receives.
type pushEndpoint struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	status   map[string]int // by path; default 201
}

func newPushEndpoint(t *testing.T) *pushEndpoint {
	t.Helper()
	p := &pushEndpoint{status: map[string]int{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.requests = append(p.requests, r)
		p.bodies = append(p.bodies, b)
		status := p.status[r.URL.Path]
		p.mu.Unlock()
		if status == 0 {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pushEndpoint) reqCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func newService(t *testing.T, db *store.Store) *Service {
	t.Helper()
	s, err := New(t.Context(), Options{Store: db, Subject: "mailto:test@example.com", AllowInsecureEndpoints: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVAPIDKeysArePersisted(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	first := newService(t, db)
	if raw, err := base64.RawURLEncoding.DecodeString(first.PublicKey()); err != nil || len(raw) != 65 {
		t.Fatalf("public key %q: %v, %d bytes", first.PublicKey(), err, len(raw))
	}
	if again := newService(t, db); again.PublicKey() != first.PublicKey() {
		t.Error("a second service on the same store generated new keys")
	}
	_ = db.Close()

	// And across a restart.
	reopened := openStore(t, dir)
	if got := newService(t, reopened); got.PublicKey() != first.PublicKey() {
		t.Error("keys changed across a restart")
	}

	// A lone half of the pair is replaced as a pair.
	other := openStore(t, t.TempDir())
	if err := other.SetSetting(t.Context(), settingPublic, "orphan"); err != nil {
		t.Fatal(err)
	}
	s := newService(t, other)
	priv, _ := other.GetSetting(t.Context(), settingPrivate)
	pub, _ := other.GetSetting(t.Context(), settingPublic)
	if s.PublicKey() == "orphan" || pub != s.PublicKey() || priv == "" {
		t.Errorf("half-stored keys not replaced: %q %q", pub, priv)
	}
}

func TestSendDeliversWithVAPIDAndTTL(t *testing.T) {
	ep := newPushEndpoint(t)
	s := newService(t, openStore(t, t.TempDir()))
	if err := s.Subscribe(t.Context(), subscription(t, ep.srv.URL+"/a")); err != nil {
		t.Fatal(err)
	}

	res, err := s.Send(t.Context(), Payload{Title: "Reminder", Body: "Stretch", URL: "/", Tag: "reminder-1"}, UrgencyHigh)
	if err != nil || res != (Result{Sent: 1}) {
		t.Fatalf("send: %v %+v", err, res)
	}
	r := ep.requests[0]
	if r.Header.Get("TTL") != "86400" || r.Header.Get("Urgency") != "high" || r.Header.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("headers = %v", r.Header)
	}
	if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "vapid t=") || !strings.Contains(auth, "k="+s.PublicKey()) {
		t.Errorf("authorization = %q", auth)
	}
	if strings.Contains(string(ep.bodies[0]), "Stretch") {
		t.Error("payload was not encrypted")
	}
}

func TestGoneSubscriptionsAreDeleted(t *testing.T) {
	ep := newPushEndpoint(t)
	ep.status["/gone"] = http.StatusGone
	ep.status["/missing"] = http.StatusNotFound
	ep.status["/broken"] = http.StatusInternalServerError
	s := newService(t, openStore(t, t.TempDir()))
	for _, path := range []string{"/ok", "/gone", "/missing", "/broken"} {
		if err := s.Subscribe(t.Context(), subscription(t, ep.srv.URL+path)); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.Send(t.Context(), Payload{Title: "t"}, UrgencyNormal)
	if err != nil || res != (Result{Sent: 1, Removed: 2, Failed: 1}) {
		t.Fatalf("send: %v %+v", err, res)
	}
	var left []string
	rows, err := s.db.QueryContext(t.Context(), `SELECT endpoint FROM push_subscriptions ORDER BY endpoint`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		left = append(left, strings.TrimPrefix(e, ep.srv.URL))
	}
	if len(left) != 2 || left[0] != "/broken" || left[1] != "/ok" {
		t.Errorf("subscriptions left = %v (a 5xx must not delete)", left)
	}
}

func TestSubscribeValidation(t *testing.T) {
	s, err := New(t.Context(), Options{Store: openStore(t, t.TempDir())}) // https only
	if err != nil {
		t.Fatal(err)
	}
	good := subscription(t, "https://push.example.com/abc")
	if err := s.Subscribe(t.Context(), good); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe(t.Context(), good); err != nil { // refresh, not a duplicate
		t.Fatal(err)
	}
	if n, _ := s.Count(t.Context()); n != 1 {
		t.Errorf("count = %d", n)
	}

	bad := map[string]func(*Subscription){
		"http endpoint":  func(s *Subscription) { s.Endpoint = "http://push.example.com/x" },
		"no endpoint":    func(s *Subscription) { s.Endpoint = "" },
		"not a url":      func(s *Subscription) { s.Endpoint = "::" },
		"short p256dh":   func(s *Subscription) { s.Keys.P256dh = "AAAA" },
		"garbage p256dh": func(s *Subscription) { s.Keys.P256dh = "!!!" },
		"no auth":        func(s *Subscription) { s.Keys.Auth = "" },
	}
	for name, mutate := range bad {
		sub := subscription(t, "https://push.example.com/other")
		mutate(&sub)
		if err := s.Subscribe(t.Context(), sub); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Padded base64url from older browsers is fine.
	padded := subscription(t, "https://push.example.com/padded")
	padded.Keys.Auth += "=="
	if err := s.Subscribe(t.Context(), padded); err != nil {
		t.Errorf("padded key rejected: %v", err)
	}
}

func TestREST(t *testing.T) {
	ep := newPushEndpoint(t)
	svc := newService(t, openStore(t, t.TempDir()))
	mux := http.NewServeMux()
	svc.Routes(mux)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	w := do("GET", "/api/push/key", "")
	var key struct {
		PublicKey string `json:"public_key"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &key) != nil || key.PublicKey != svc.PublicKey() {
		t.Fatalf("key: %d %s", w.Code, w.Body)
	}

	// Nobody subscribed: say so rather than pretending.
	if w := do("POST", "/api/push/test", ""); w.Code != http.StatusConflict {
		t.Errorf("test with no devices = %d %s", w.Code, w.Body)
	}

	// The app posts PushSubscription.toJSON(), which has expirationTime. The
	// REST layer demands https, so this one goes straight to the store.
	sub := subscription(t, ep.srv.URL+"/device")
	body, _ := json.Marshal(map[string]any{"endpoint": "http://push.example.com/x", "expirationTime": nil,
		"keys": map[string]string{"p256dh": sub.Keys.P256dh, "auth": sub.Keys.Auth}})
	if w := do("POST", "/api/push/subscriptions", string(body)); w.Code != 204 {
		// svc allows insecure endpoints in tests, so this succeeds too.
		t.Fatalf("subscribe = %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/push/subscriptions", `{"endpoint":"https://x.example/","keys":{"p256dh":"AA","auth":"AA"}}`); w.Code != 400 {
		t.Errorf("bad keys = %d", w.Code)
	}
	if w := do("POST", "/api/push/subscriptions", `{"endpoint":"https://x.example/","bogus":1}`); w.Code != 400 {
		t.Errorf("unknown field = %d", w.Code)
	}
	if w := do("DELETE", "/api/push/subscriptions", `{"endpoint":"http://push.example.com/x"}`); w.Code != 204 {
		t.Errorf("unsubscribe = %d", w.Code)
	}
	if w := do("DELETE", "/api/push/subscriptions", `{}`); w.Code != 400 {
		t.Errorf("unsubscribe without endpoint = %d", w.Code)
	}

	if err := svc.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if w := do("POST", "/api/push/test", ""); w.Code != 204 || ep.reqCount() != 1 {
		t.Errorf("test = %d, requests %d", w.Code, ep.reqCount())
	}
	ep.mu.Lock()
	ep.status["/device"] = http.StatusServiceUnavailable
	ep.mu.Unlock()
	if w := do("POST", "/api/push/test", ""); w.Code != http.StatusBadGateway {
		t.Errorf("test against a failing service = %d", w.Code)
	}
}

func TestHTTPSRequiredByDefault(t *testing.T) {
	s, err := New(t.Context(), Options{Store: openStore(t, t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	sub := subscription(t, "http://push.example.com/x")
	b, _ := json.Marshal(sub)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/push/subscriptions", strings.NewReader(string(b))))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "https") {
		t.Errorf("http endpoint = %d %s", w.Code, w.Body)
	}
}
