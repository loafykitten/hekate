package money

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

const (
	testClientID = "client-id-123"
	testSecret   = "plaid-secret-456"
	testAccess   = "fake-access-1"
)

// fakePlaid is a test double for Plaid's API. Each endpoint answers through a
// handler that gets the decoded request body; calls are recorded in order.
type fakePlaid struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	calls    []call
	handlers map[string]func(req map[string]any) (int, any)
}

type call struct {
	path string
	req  map[string]any
}

func newFakePlaid(t *testing.T) *fakePlaid {
	f := &fakePlaid{t: t, handlers: map[string]func(map[string]any) (int, any){}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	// Sensible defaults: one account, nothing to sync.
	f.handle(pathExchange, func(map[string]any) (int, any) {
		return 200, map[string]any{"access_token": testAccess, "item_id": "plaid-item-1"}
	})
	f.handle(pathAccounts, func(map[string]any) (int, any) { return 200, map[string]any{"accounts": []any{checking(1520.31)}} })
	f.handle(pathSync, func(map[string]any) (int, any) {
		return 200, map[string]any{"added": []any{}, "modified": []any{}, "removed": []any{}, "next_cursor": "c0", "has_more": false,
			"transactions_update_status": "HISTORICAL_UPDATE_COMPLETE"}
	})
	f.handle(pathItemRemove, func(map[string]any) (int, any) { return 200, map[string]any{} })
	f.handle(pathLinkCreate, func(map[string]any) (int, any) { return 200, map[string]any{"link_token": "link-sandbox-abc"} })
	return f
}

func (f *fakePlaid) handle(path string, h func(req map[string]any) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

func (f *fakePlaid) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(b, &req); err != nil {
		f.t.Errorf("%s: body is not JSON: %v", r.URL.Path, err)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{r.URL.Path, req})
	h := f.handlers[r.URL.Path]
	f.mu.Unlock()
	if h == nil {
		http.NotFound(w, r)
		return
	}
	status, body := h(req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// callsTo returns the recorded requests to path.
func (f *fakePlaid) callsTo(path string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.path == path {
			out = append(out, c.req)
		}
	}
	return out
}

func plaidErr(status int, typ, code, msg string) (int, any) {
	return status, map[string]any{"error_type": typ, "error_code": code, "error_message": msg, "display_message": nil}
}

func checking(current float64) map[string]any {
	return map[string]any{
		"account_id": "acc-1", "name": "Spending", "official_name": "Example Bank Spending", "mask": "1234",
		"type": "depository", "subtype": "checking",
		"balances": map[string]any{"current": current, "available": current - 30, "limit": nil, "iso_currency_code": "USD"},
	}
}

func creditCard(id string, owed, limit float64) map[string]any {
	return map[string]any{
		"account_id": id, "name": "Example Card", "official_name": nil, "mask": "9876", "type": "credit", "subtype": "credit card",
		"balances": map[string]any{"current": owed, "available": limit - owed, "limit": limit, "iso_currency_code": "USD"},
	}
}

// txn builds a Plaid transaction; amount is Plaid's: positive for money out.
func txn(id, date, name, merchant string, amount float64, category string, pending bool) map[string]any {
	m := map[string]any{
		"transaction_id": id, "account_id": "acc-1", "date": date, "authorized_date": nil, "name": name,
		"amount": amount, "iso_currency_code": "USD", "pending": pending, "payment_channel": "in store",
	}
	if merchant != "" {
		m["merchant_name"] = merchant
	}
	if category != "" {
		m["personal_finance_category"] = map[string]any{"primary": category, "detailed": category + "_OTHER"}
	}
	return m
}

type events struct {
	mu      sync.Mutex
	domains []string
}

func (e *events) Invalidate(domains ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.domains = append(e.domains, domains...)
}

func (e *events) Notify(proto.Notify) {}

func (e *events) count(domain string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, d := range e.domains {
		if d == domain {
			n++
		}
	}
	return n
}

type fixture struct {
	t      *testing.T
	svc    *Service
	plaid  *fakePlaid
	events *events
	store  *store.Store
}

// newFixture builds a configured Service against the fake Plaid. The clock is
// 2026-10-20 14:00 in New York.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, plaid: newFakePlaid(t), events: &events{}, store: st}
	f.svc, err = New(Options{
		Store: st, ClientID: testClientID, Secret: testSecret, Env: "sandbox", BaseURL: f.plaid.srv.URL,
		RedirectURI: "https://hekate.example.com/money/oauth", Location: loc, Events: f.events,
		Now: func() time.Time { return time.Date(2026, 10, 20, 14, 0, 0, 0, loc) },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.afterConnect = nil // no syncs outliving the test
	t.Cleanup(f.svc.wg.Wait)
	return f
}

// connect links a bank and waits for its first sync.
func (f *fixture) connect() Item {
	f.t.Helper()
	it, err := f.svc.Connect(f.t.Context(), "public-sandbox-xyz", "ins_1", "Example Bank", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.svc.wg.Wait()
	return it
}

func (f *fixture) do(method, path, body string) (int, string) {
	f.t.Helper()
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	f.svc.wg.Wait()
	return rec.Code, rec.Body.String()
}

// syncPages makes /transactions/sync answer from pages keyed by the cursor asked for.
func (f *fakePlaid) syncPages(pages map[string]map[string]any) {
	f.handle(pathSync, func(req map[string]any) (int, any) {
		cursor, _ := req["cursor"].(string)
		p, ok := pages[cursor]
		if !ok {
			f.t.Errorf("sync asked for unknown cursor %q", cursor)
			return 400, map[string]any{}
		}
		return 200, p
	})
}

func page(added, modified []any, removed []string, next string, more bool) map[string]any {
	rm := []any{}
	for _, id := range removed {
		rm = append(rm, map[string]any{"transaction_id": id})
	}
	if added == nil {
		added = []any{}
	}
	if modified == nil {
		modified = []any{}
	}
	return map[string]any{"added": added, "modified": modified, "removed": rm, "next_cursor": next, "has_more": more,
		"transactions_update_status": "HISTORICAL_UPDATE_COMPLETE"}
}
