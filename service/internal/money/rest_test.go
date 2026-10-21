package money

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return out
}

// seed connects a bank with a few transactions in two accounts.
func seed(f *fixture) Item {
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) {
		return 200, map[string]any{"accounts": []any{checking(1520.31), creditCard("acc-2", 300, 2000)}}
	})
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{
			txn("t1", "2026-10-19", "SQ *BLUE BOTTLE", "Blue Bottle Coffee", 6.75, "FOOD_AND_DRINK", false),
			txn("t2", "2026-10-19", "SQ *BLUE BOTTLE", "Blue Bottle Coffee", 4.5, "FOOD_AND_DRINK", true),
			txn("t3", "2026-10-15", "PAYROLL", "", -2400, "INCOME", false),
			txn("t4", "2026-09-30", "METRO TRANSIT", "Metro", 2.5, "TRANSPORTATION", false),
			txn("t5", "2026-10-02", "100% DONE_DEAL", "", 20, "ENTERTAINMENT", false),
		}, nil, nil, "c1", false),
	})
	return f.connect()
}

func TestRESTStatusAndLists(t *testing.T) {
	f := newFixture(t)
	code, body := f.do("GET", "/api/money/status", "")
	if out := decode(t, body); code != 200 || out["configured"] != true || len(out["items"].([]any)) != 0 {
		t.Errorf("empty status = %d %s", code, body)
	}
	seed(f)
	code, body = f.do("GET", "/api/money/status", "")
	item := decode(t, body)["items"].([]any)[0].(map[string]any)
	if code != 200 || item["id"] != "1" || item["institution"] != "Example Bank" || item["status"] != "ok" || item["error"] != "" ||
		item["accounts"] != float64(2) || item["last_sync_at"] != "2026-10-20T14:00:00-04:00" {
		t.Errorf("status = %d %s", code, body)
	}

	code, body = f.do("GET", "/api/money/accounts", "")
	accts := decode(t, body)["accounts"].([]any)
	if code != 200 || len(accts) != 2 {
		t.Fatalf("accounts = %d %s", code, body)
	}
	// By institution, then name: "Example Card" before "Spending".
	card, spend := accts[0].(map[string]any), accts[1].(map[string]any)
	bal := spend["balance"].(map[string]any)
	if card["name"] != "Example Card" || spend["id"] != "acc-1" || spend["item_id"] != "1" || spend["institution"] != "Example Bank" ||
		spend["type"] != "depository" || spend["currency"] != "USD" || bal["current"] != 1520.31 || bal["available"] != 1490.31 || bal["limit"] != nil ||
		card["balance"].(map[string]any)["limit"] != 2000.0 {
		t.Errorf("accounts = %s", body)
	}

	code, body = f.do("GET", "/api/money/transactions", "")
	txns := decode(t, body)["transactions"].([]any)
	if code != 200 || len(txns) != 5 {
		t.Fatalf("transactions = %d %s", code, body)
	}
	first := txns[0].(map[string]any) // pending first within the day
	if first["id"] != "t2" || first["pending"] != true || first["amount"] != -4.5 || first["account"] != "Spending" ||
		first["merchant"] != "Blue Bottle Coffee" || first["category_label"] != "Food and drink" || first["detail"] != "FOOD_AND_DRINK_OTHER" {
		t.Errorf("first = %v", first)
	}

	for _, tt := range []struct {
		query string
		want  int
	}{
		{"q=blue", 2}, {"q=bottle&category=Food+and+drink", 2}, {"category=INCOME", 1}, {"account=acc-2", 0}, {"account=acc-1", 5},
		{"from=2026-10-15&to=2026-10-19", 3}, {"limit=2", 2}, {"q=100%25", 1}, {"q=_", 1}, {"q=%25", 1},
	} {
		code, body := f.do("GET", "/api/money/transactions?"+tt.query, "")
		if got := len(decode(t, body)["transactions"].([]any)); code != 200 || got != tt.want {
			t.Errorf("%s: %d results (%d), want %d", tt.query, got, code, tt.want)
		}
	}
	for _, q := range []string{"limit=0", "limit=x", "from=yesterday", "to=2026-13-01"} {
		if code, _ := f.do("GET", "/api/money/transactions?"+q, ""); code != 400 {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
	if code, _ := f.do("GET", "/api/money/transactions?limit=9999", ""); code != 200 {
		t.Errorf("a large limit is capped, not refused: %d", code)
	}
}

func TestRESTSpending(t *testing.T) {
	f := newFixture(t)
	seed(f)
	code, body := f.do("GET", "/api/money/spending", "")
	out := decode(t, body)
	// October: coffee 11.25 + entertainment 20; September: transit 2.50.
	if code != 200 || out["month"] != "2026-10" || out["currency"] != "USD" || out["spent"] != 31.25 || out["received"] != 2400.0 ||
		out["previous_spent"] != 2.5 || out["transfers"] != 0.0 {
		t.Fatalf("spending = %d %s", code, body)
	}
	cats := out["categories"].([]any)
	if len(cats) != 2 || cats[0].(map[string]any)["category"] != "ENTERTAINMENT" || cats[1].(map[string]any)["count"] != 2.0 {
		t.Errorf("categories = %v", cats)
	}
	_, body = f.do("GET", "/api/money/spending?month=2026-09", "")
	if out := decode(t, body); out["spent"] != 2.5 || out["month"] != "2026-09" {
		t.Errorf("september = %s", body)
	}
	if code, _ := f.do("GET", "/api/money/spending?month=sept", ""); code != 400 {
		t.Errorf("bad month = %d", code)
	}
}

func TestRESTLinkConnectRemoveSync(t *testing.T) {
	f := newFixture(t)
	code, body := f.do("POST", "/api/money/link-token", "")
	if code != 200 || decode(t, body)["link_token"] != "link-sandbox-abc" {
		t.Errorf("link token = %d %s", code, body)
	}
	if code, _ := f.do("POST", "/api/money/link-token", `{"item_id":"7"}`); code != 404 {
		t.Errorf("update link token for an unknown bank = %d", code)
	}
	if code, _ := f.do("POST", "/api/money/items", `{"institution":{"id":"x","name":"Y"}}`); code != 400 {
		t.Errorf("connect without a token = %d", code)
	}
	code, body = f.do("POST", "/api/money/items", `{"public_token":"public-1","institution":{"id":"ins_1","name":"Example Bank"}}`)
	item := decode(t, body)
	if code != 200 || item["id"] != "1" || item["institution"] != "Example Bank" || item["status"] != "ok" {
		t.Fatalf("connect = %d %s", code, body)
	}
	if n := len(f.plaid.callsTo(pathSync)); n != 1 {
		t.Errorf("the first sync made %d sync calls, want 1", n)
	}
	if code, _ := f.do("POST", "/api/money/sync", ""); code != 202 {
		t.Errorf("sync = %d", code)
	}
	if n := len(f.plaid.callsTo(pathSync)); n != 2 {
		t.Errorf("sync made %d calls in all, want 2", n)
	}
	if code, _ := f.do("DELETE", "/api/money/items/x", ""); code != 400 {
		t.Errorf("delete x = %d", code)
	}
	if code, _ := f.do("DELETE", "/api/money/items/1", ""); code != 204 {
		t.Errorf("delete = %d", code)
	}
	if code, _ := f.do("DELETE", "/api/money/items/1", ""); code != 404 {
		t.Errorf("delete again = %d", code)
	}
	f.plaid.handle(pathExchange, func(map[string]any) (int, any) {
		return plaidErr(400, "INVALID_INPUT", "INVALID_PUBLIC_TOKEN", "bad token")
	})
	if code, body := f.do("POST", "/api/money/items", `{"public_token":"x","institution":{"id":"","name":""}}`); code != 502 || strings.Contains(body, "bad token") {
		t.Errorf("failed exchange = %d %s", code, body)
	}
}

// Whatever the routes return, an access token or key is never in it.
func TestRESTNeverReturnsSecrets(t *testing.T) {
	f := newFixture(t)
	seed(f)
	f.plaid.handle(pathSync, func(map[string]any) (int, any) {
		return plaidErr(400, "ITEM_ERROR", "ITEM_LOGIN_REQUIRED", "login changed")
	})
	_, _ = f.svc.SyncAll(t.Context())
	for _, req := range [][2]string{
		{"GET", "/api/money/status"}, {"GET", "/api/money/accounts"}, {"GET", "/api/money/transactions"}, {"GET", "/api/money/spending"},
		{"POST", "/api/money/link-token"}, {"POST", "/api/money/sync"},
	} {
		code, body := f.do(req[0], req[1], "")
		for _, secret := range []string{testAccess, testSecret, testClientID, "plaid-item-1", "access_token", "public-sandbox"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s %s (%d) contains %q: %s", req[0], req[1], code, secret, body)
			}
		}
	}
	code, body := f.do("POST", "/api/money/items", `{"public_token":"public-sandbox-xyz","institution":{"id":"ins_1","name":"Example Bank"}}`)
	for _, secret := range []string{testAccess, testSecret, "plaid-item-1", "access_token", "public-sandbox"} {
		if strings.Contains(body, secret) {
			t.Errorf("connect (%d) contains %q: %s", code, secret, body)
		}
	}
}

func TestRESTUnconfigured(t *testing.T) {
	f := newFixture(t)
	seed(f)
	f.svc.plaid = nil
	get := func(path string) (int, map[string]any) {
		mux := http.NewServeMux()
		f.svc.Routes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := get("/api/money/status"); code != 200 || out["configured"] != false || len(out["items"].([]any)) != 0 {
		t.Errorf("status = %d %v", code, out)
	}
	if code, out := get("/api/money/accounts"); code != 200 || len(out["accounts"].([]any)) != 0 {
		t.Errorf("accounts = %d %v", code, out)
	}
	if code, out := get("/api/money/transactions"); code != 200 || len(out["transactions"].([]any)) != 0 {
		t.Errorf("transactions = %d %v", code, out)
	}
	if code, out := get("/api/money/spending"); code != 200 || out["spent"] != 0.0 || len(out["categories"].([]any)) != 0 {
		t.Errorf("spending = %d %v", code, out)
	}
	if code, _ := f.do("POST", "/api/money/link-token", ""); code != 503 {
		t.Errorf("link token = %d, want 503", code)
	}
	if code, _ := f.do("POST", "/api/money/sync", ""); code != 503 {
		t.Errorf("sync = %d, want 503", code)
	}
	if code, _ := f.do("POST", "/api/money/items", `{"public_token":"p","institution":{"id":"","name":""}}`); code != 503 {
		t.Errorf("connect = %d, want 503", code)
	}
	if code, _ := f.do("DELETE", "/api/money/items/1", ""); code != 503 {
		t.Errorf("remove = %d, want 503", code)
	}
}
