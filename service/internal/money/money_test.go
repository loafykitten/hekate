package money

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/jobs"
)

func TestLinkToken(t *testing.T) {
	f := newFixture(t)
	tok, err := f.svc.LinkToken(t.Context(), "")
	if err != nil || tok != "link-sandbox-abc" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	req := f.plaid.callsTo(pathLinkCreate)[0]
	user := req["user"].(map[string]any)
	if req["client_name"] != "Hekate" || user["client_user_id"] != "owner" || req["language"] != "en" ||
		req["redirect_uri"] != "https://hekate.example.com/money/oauth" || req["client_id"] != testClientID || req["secret"] != testSecret {
		t.Errorf("new-bank request = %v", req)
	}
	if p, _ := json.Marshal(req["products"]); string(p) != `["transactions"]` {
		t.Errorf("products = %s", p)
	}
	if c, _ := json.Marshal(req["country_codes"]); string(c) != `["US"]` {
		t.Errorf("country_codes = %s", c)
	}
	if req["transactions"].(map[string]any)["days_requested"] != float64(730) {
		t.Errorf("transactions = %v", req["transactions"])
	}
	if _, has := req["access_token"]; has {
		t.Error("a new-bank link token carries an access token")
	}

	// Update mode: the bank's access token, and no products.
	it := f.connect()
	if _, err := f.svc.LinkToken(t.Context(), it.ID); err != nil {
		t.Fatal(err)
	}
	req = f.plaid.callsTo(pathLinkCreate)[1]
	if req["access_token"] != testAccess {
		t.Errorf("update request has no access token: %v", req)
	}
	if _, has := req["products"]; has {
		t.Errorf("update mode must not pass products: %v", req)
	}
	if _, err := f.svc.LinkToken(t.Context(), "999"); err == nil {
		t.Error("link token for an unknown bank")
	}
}

func TestLinkTokenPlaidFailure(t *testing.T) {
	f := newFixture(t)
	f.plaid.handle(pathLinkCreate, func(map[string]any) (int, any) {
		return plaidErr(400, "INVALID_REQUEST", "INVALID_FIELD", "bad")
	})
	_, err := f.svc.LinkToken(t.Context(), "")
	if err == nil || err.Error() != errPlaid().Msg {
		t.Errorf("err = %v", err)
	}
}

func TestConnectAndFirstSync(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{
			txn("t1", "2026-10-19", "SQ *BLUE BOTTLE", "Blue Bottle Coffee", 6.75, "FOOD_AND_DRINK", false),
			txn("t2", "2026-10-15", "PAYROLL", "", -2400, "INCOME", false),
			txn("t3", "2026-10-20", "CORNER STORE", "", 12.5, "", true),
		}, nil, nil, "c1", false),
	})
	it := f.connect()
	if it.Institution != "Example Bank" || it.Status != StatusOK || it.ID != "1" {
		t.Errorf("item = %+v", it)
	}
	items, _ := f.svc.Items(t.Context())
	if len(items) != 1 || items[0].Accounts != 1 || items[0].LastSyncAt == nil || items[0].Error != "" {
		t.Errorf("items = %+v", items)
	}
	txns, err := f.svc.Transactions(t.Context(), Query{})
	if err != nil || len(txns) != 3 {
		t.Fatalf("transactions = %v, %v", txns, err)
	}
	// Newest first; the sign is money in (+) / out (-).
	if txns[0].ID != "t3" || txns[0].Amount != -12.5 || !txns[0].Pending || txns[0].Category != nil || txns[0].CategoryLabel != nil {
		t.Errorf("t3 = %+v", txns[0])
	}
	if txns[1].ID != "t1" || txns[1].Amount != -6.75 || *txns[1].Merchant != "Blue Bottle Coffee" ||
		*txns[1].Category != "FOOD_AND_DRINK" || *txns[1].CategoryLabel != "Food and drink" || *txns[1].Detail != "FOOD_AND_DRINK_OTHER" ||
		txns[1].Account != "Spending" || txns[1].Currency != "USD" {
		t.Errorf("t1 = %+v", txns[1])
	}
	if txns[2].Amount != 2400 || txns[2].Merchant != nil {
		t.Errorf("t2 = %+v", txns[2])
	}
	accts, _ := f.svc.Accounts(t.Context())
	if len(accts) != 1 || *accts[0].Balance.Current != 1520.31 || *accts[0].Balance.Available != 1490.31 || accts[0].Balance.Limit != nil ||
		accts[0].Institution != "Example Bank" || accts[0].Mask != "1234" || accts[0].Subtype != "checking" {
		t.Errorf("accounts = %+v", accts)
	}
	if f.events.count("money") == 0 {
		t.Error("no invalidate event")
	}
}

func TestSyncResumesFromSavedCursor(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"":   page([]any{txn("t1", "2026-10-19", "A", "", 1, "GENERAL_SERVICES", false)}, nil, nil, "c1", false),
		"c1": page(nil, nil, nil, "c1", false),
	})
	f.connect()
	res, err := f.svc.SyncAll(t.Context())
	if err != nil || res.Banks != 1 || res.Added != 0 {
		t.Fatalf("second sync = %+v, %v", res, err)
	}
	calls := f.plaid.callsTo(pathSync)
	if _, has := calls[0]["cursor"]; has || calls[1]["cursor"] != "c1" || calls[0]["count"] != float64(500) {
		t.Errorf("sync requests = %v", calls)
	}
}

func TestSyncPaginatesAndAppliesOnlyWhenComplete(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"":   page([]any{txn("t1", "2026-10-01", "A", "", 1, "GENERAL_SERVICES", false)}, nil, nil, "c1", true),
		"c1": page([]any{txn("t2", "2026-10-02", "B", "", 2, "GENERAL_SERVICES", false)}, nil, nil, "c2", true),
		"c2": page([]any{txn("t3", "2026-10-03", "C", "", 3, "GENERAL_SERVICES", false)}, nil, nil, "c3", false),
	})
	f.connect()
	txns, _ := f.svc.Transactions(t.Context(), Query{})
	if len(txns) != 3 {
		t.Errorf("got %d transactions, want 3", len(txns))
	}
	var cursor string
	if err := f.store.DB().QueryRow(`SELECT cursor FROM money_items`).Scan(&cursor); err != nil || cursor != "c3" {
		t.Errorf("cursor = %q, %v", cursor, err)
	}
}

func TestSyncRestartsOnMutationDuringPagination(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"":   page([]any{txn("t1", "2026-10-01", "A", "", 1, "GENERAL_SERVICES", false)}, nil, nil, "c1", false),
		"c1": page([]any{txn("t2", "2026-10-02", "B", "", 2, "GENERAL_SERVICES", false)}, nil, nil, "c2", true),
		"c2": page(nil, nil, nil, "c3", false),
	})
	f.connect()

	// Second sync: page one answers, page two hits a mutation once.
	failed := false
	var asked []string
	f.plaid.handle(pathSync, func(req map[string]any) (int, any) {
		cursor, _ := req["cursor"].(string)
		asked = append(asked, cursor)
		switch {
		case cursor == "c1":
			return 200, page([]any{txn("t4", "2026-10-04", "D", "", 4, "GENERAL_SERVICES", false)}, nil, nil, "c5", true)
		case cursor == "c5" && !failed:
			failed = true
			return plaidErr(400, "TRANSACTIONS_ERROR", "TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION", "changed")
		}
		return 200, page(nil, nil, nil, "c6", false)
	})
	// Start the pass from c1 by hand (the first connect saved c2).
	if _, err := f.store.DB().Exec(`UPDATE money_items SET cursor = 'c1'`); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.SyncAll(t.Context())
	if err != nil || res.Added != 1 {
		t.Fatalf("sync = %+v, %v", res, err)
	}
	if strings.Join(asked, ",") != "c1,c5,c1,c5" {
		t.Errorf("cursors asked = %v, want the whole pass restarted from c1", asked)
	}
	var cursor string
	_ = f.store.DB().QueryRow(`SELECT cursor FROM money_items`).Scan(&cursor)
	if cursor != "c6" {
		t.Errorf("cursor = %q", cursor)
	}
}

func TestSyncFailureKeepsCursorAndRows(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{txn("t1", "2026-10-01", "A", "", 1, "GENERAL_SERVICES", false)}, nil, nil, "c1", false),
	})
	f.connect()
	f.plaid.handle(pathSync, func(req map[string]any) (int, any) {
		if req["cursor"] == "c1" {
			return 200, page([]any{txn("t2", "2026-10-02", "B", "", 2, "GENERAL_SERVICES", false)}, nil, []string{"t1"}, "c2", true)
		}
		return plaidErr(500, "API_ERROR", "INTERNAL_SERVER_ERROR", "boom")
	})
	if _, err := f.svc.SyncAll(t.Context()); err == nil {
		t.Fatal("sync succeeded")
	}
	txns, _ := f.svc.Transactions(t.Context(), Query{})
	if len(txns) != 1 || txns[0].ID != "t1" {
		t.Errorf("half a pass was applied: %+v", txns)
	}
	var cursor, status string
	_ = f.store.DB().QueryRow(`SELECT cursor, status FROM money_items`).Scan(&cursor, &status)
	if cursor != "c1" || status != StatusError {
		t.Errorf("cursor = %q status = %q", cursor, status)
	}
}

func TestSyncModifiedAndRemoved(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{
			txn("t1", "2026-10-19", "PENDING COFFEE", "", 5, "FOOD_AND_DRINK", true),
			txn("t2", "2026-10-18", "GONE", "", 9, "FOOD_AND_DRINK", false),
		}, nil, nil, "c1", false),
		"c1": page(
			[]any{txn("t3", "2026-10-20", "COFFEE", "Cafe", 5.25, "FOOD_AND_DRINK", false)},
			[]any{txn("t1", "2026-10-19", "PENDING COFFEE", "", 5.5, "FOOD_AND_DRINK", true)},
			[]string{"t2", "never-seen"}, "c2", false),
	})
	f.connect()
	res, err := f.svc.SyncAll(t.Context())
	if err != nil || res.Added != 1 {
		t.Fatalf("sync = %+v, %v", res, err)
	}
	txns, _ := f.svc.Transactions(t.Context(), Query{})
	if len(txns) != 2 || txns[0].ID != "t3" || txns[1].ID != "t1" || txns[1].Amount != -5.5 {
		t.Errorf("transactions = %+v", txns)
	}
}

func TestLoginRequiredAndRecovery(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.plaid.handle(pathSync, func(map[string]any) (int, any) {
		return plaidErr(400, "ITEM_ERROR", "ITEM_LOGIN_REQUIRED", "the login details of this item have changed")
	})
	res, err := f.svc.SyncAll(t.Context())
	if err == nil || res.Failed != 1 {
		t.Fatalf("sync = %+v, %v", res, err)
	}
	items, _ := f.svc.Items(t.Context())
	if items[0].Status != StatusLoginRequired || items[0].Error == "" {
		t.Errorf("item = %+v", items[0])
	}
	if strings.Contains(err.Error(), testAccess) || strings.Contains(err.Error(), testSecret) {
		t.Errorf("error leaks a credential: %v", err)
	}

	f.plaid.handle(pathSync, func(map[string]any) (int, any) { return 200, page(nil, nil, nil, "c9", false) })
	if _, err := f.svc.SyncAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = f.svc.Items(t.Context())
	if items[0].Status != StatusOK || items[0].Error != "" {
		t.Errorf("after recovery: %+v", items[0])
	}
}

func TestOtherErrorsMarkError(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) {
		return plaidErr(500, "API_ERROR", "INTERNAL_SERVER_ERROR", "something broke")
	})
	if _, err := f.svc.SyncAll(t.Context()); err == nil {
		t.Fatal("sync succeeded")
	}
	items, _ := f.svc.Items(t.Context())
	if items[0].Status != StatusError || items[0].Error != "something broke" {
		t.Errorf("item = %+v", items[0])
	}
}

func TestRemoveItem(t *testing.T) {
	f := newFixture(t)
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{txn("t1", "2026-10-19", "A", "", 1, "GENERAL_SERVICES", false)}, nil, nil, "c1", false),
	})
	it := f.connect()

	f.plaid.handle(pathItemRemove, func(map[string]any) (int, any) { return plaidErr(500, "API_ERROR", "INTERNAL_SERVER_ERROR", "x") })
	if err := f.svc.Remove(t.Context(), it.ID); err == nil {
		t.Error("removal succeeded though Plaid failed")
	}
	if items, _ := f.svc.Items(t.Context()); len(items) != 1 {
		t.Error("the bank was dropped locally though Plaid still has it")
	}

	f.plaid.handle(pathItemRemove, func(map[string]any) (int, any) { return 200, map[string]any{} })
	if err := f.svc.Remove(t.Context(), it.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.plaid.callsTo(pathItemRemove); got[len(got)-1]["access_token"] != testAccess {
		t.Errorf("remove request = %v", got)
	}
	for _, table := range []string{"money_items", "money_accounts", "money_transactions"} {
		var n int
		if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s has %d rows after removal (%v)", table, n, err)
		}
	}
	if err := f.svc.Remove(t.Context(), it.ID); err == nil {
		t.Error("removed a bank twice")
	}
	if err := f.svc.Remove(t.Context(), "x"); err == nil {
		t.Error("removed bank x")
	}
}

func TestRemoveItemAlreadyGoneAtPlaid(t *testing.T) {
	f := newFixture(t)
	it := f.connect()
	f.plaid.handle(pathItemRemove, func(map[string]any) (int, any) {
		return plaidErr(400, "ITEM_ERROR", "ITEM_NOT_FOUND", "gone")
	})
	if err := f.svc.Remove(t.Context(), it.ID); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestReconnectingSameBankReplacesToken(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.plaid.handle(pathSync, func(map[string]any) (int, any) {
		return plaidErr(400, "ITEM_ERROR", "ITEM_LOGIN_REQUIRED", "x")
	})
	_, _ = f.svc.SyncAll(t.Context())
	f.plaid.handle(pathSync, func(map[string]any) (int, any) { return 200, page(nil, nil, nil, "c9", false) })
	it := f.connect()
	if it.ID != "1" || it.Status != StatusOK {
		t.Errorf("item = %+v", it)
	}
	if items, _ := f.svc.Items(t.Context()); len(items) != 1 {
		t.Errorf("items = %+v", items)
	}
}

func TestJob(t *testing.T) {
	f := newFixture(t)
	job := f.svc.Job()
	if job.Name != "money" {
		t.Errorf("name = %q", job.Name)
	}
	if _, err := job.Run(t.Context()); !errors.Is(err, jobs.ErrSkip) {
		t.Errorf("no banks: %v", err)
	}
	f.plaid.syncPages(map[string]map[string]any{
		"": page([]any{
			txn("t1", "2026-10-19", "A", "", 1, "GENERAL_SERVICES", false),
			txn("t2", "2026-10-19", "B", "", 2, "GENERAL_SERVICES", false),
		}, nil, nil, "c1", false),
		"c1": page(nil, nil, nil, "c1", false),
	})
	f.connect()
	if _, err := f.svc.Connect(t.Context(), "public-2", "ins_2", "Other Bank"); err != nil {
		t.Fatal(err)
	}
	f.svc.wg.Wait()
	// Both connects share one item id in the fake; make the second a real second bank.
	if _, err := f.store.DB().Exec(`INSERT INTO money_items (plaid_item_id, access_token, institution_name, cursor, created_at) VALUES ('plaid-item-2', 'tok2', 'Other Bank', 'c1', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	summary, err := job.Run(t.Context())
	if err != nil || summary != "Synced 2 banks: no new transactions" {
		t.Errorf("summary = %q, %v", summary, err)
	}

	if s := (SyncResult{Banks: 2, Added: 14}).summary(); s != "Synced 2 banks: 14 new transactions" {
		t.Errorf("summary = %q", s)
	}
	if s := (SyncResult{Banks: 1, Added: 1}).summary(); s != "Synced 1 bank: 1 new transaction" {
		t.Errorf("summary = %q", s)
	}
	if s := (SyncResult{Banks: 2, Failed: 1, Added: 3}).summary(); s != "Synced 1 bank: 3 new transactions; 1 need attention" {
		t.Errorf("summary = %q", s)
	}
}

func TestUnconfigured(t *testing.T) {
	f := newFixture(t)
	f.svc.plaid = nil
	if f.svc.Configured() {
		t.Fatal("configured")
	}
	if _, err := f.svc.Job().Run(t.Context()); !errors.Is(err, jobs.ErrSkip) {
		t.Errorf("job = %v", err)
	}
	if _, err := f.svc.LinkToken(t.Context(), ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("link token = %v", err)
	}
	if _, err := f.svc.Connect(t.Context(), "p", "", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("connect = %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	f := newFixture(t)
	if _, err := New(Options{}); err == nil {
		t.Error("New accepted a missing store")
	}
	if _, err := New(Options{Store: f.store, ClientID: "x"}); err == nil {
		t.Error("New accepted a client id without a secret")
	}
	s, err := New(Options{Store: f.store, ClientID: "x", Secret: "y"})
	if err != nil || s.plaid.base != productionURL {
		t.Errorf("default base = %v, %v", s.plaid, err)
	}
	s, _ = New(Options{Store: f.store, ClientID: "x", Secret: "y", Env: "sandbox"})
	if s.plaid.base != sandboxURL {
		t.Errorf("sandbox base = %q", s.plaid.base)
	}
	s, _ = New(Options{Store: f.store})
	if s.Configured() {
		t.Error("configured with no keys")
	}
}

func TestCents(t *testing.T) {
	for _, tt := range []struct {
		plaid float64
		want  int64
	}{{6.75, -675}, {-2400, 240000}, {0, 0}, {0.1 + 0.2, -30}, {1234.565, -123457}, {-0.01, 1}} {
		if got := cents(tt.plaid); got != tt.want {
			t.Errorf("cents(%v) = %d, want %d", tt.plaid, got, tt.want)
		}
	}
}

func TestLinkingAConnectedBankAgainIsRefused(t *testing.T) {
	f := newFixture(t)
	first := f.connect()
	// A second Link of the same bank: Plaid makes a new item, with new ids for the same accounts.
	f.plaid.handle(pathExchange, func(map[string]any) (int, any) {
		return 200, map[string]any{"access_token": "access-repeat", "item_id": "plaid-item-2"}
	})
	repeat := checking(99)
	repeat["account_id"] = "acc-repeat"
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) { return 200, map[string]any{"accounts": []any{repeat}} })
	_, err := f.svc.Connect(t.Context(), "public-sandbox-again", "ins_1", "Example Bank")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if removed := f.plaid.callsTo(pathItemRemove); len(removed) != 1 || removed[0]["access_token"] != "access-repeat" {
		t.Errorf("the repeat link wasn't removed at Plaid: %v", removed)
	}
	items, _ := f.svc.Items(t.Context())
	if len(items) != 1 || items[0].ID != first.ID {
		t.Errorf("banks = %+v", items)
	}

	// Another bank at the same institution with different accounts is fine.
	other := checking(5)
	other["account_id"], other["mask"] = "acc-other", "7777"
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) { return 200, map[string]any{"accounts": []any{other}} })
	f.plaid.handle(pathExchange, func(map[string]any) (int, any) {
		return 200, map[string]any{"access_token": "access-other", "item_id": "plaid-item-3"}
	})
	if _, err := f.svc.Connect(t.Context(), "public-sandbox-other", "ins_1", "Example Bank"); err != nil {
		t.Fatalf("a second login at the same bank was refused: %v", err)
	}
	f.svc.wg.Wait()
}

func TestAccountsTheBankStopsListingAreRetired(t *testing.T) {
	f := newFixture(t)
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) {
		return 200, map[string]any{"accounts": []any{checking(100), creditCard("acc-card", 40, 1000)}}
	})
	f.connect()
	f.plaid.handle(pathAccounts, func(map[string]any) (int, any) { return 200, map[string]any{"accounts": []any{checking(120)}} })
	if _, err := f.svc.SyncAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	accounts, err := f.svc.Accounts(t.Context())
	if err != nil || len(accounts) != 1 || accounts[0].ID != "acc-1" {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	if items, _ := f.svc.Items(t.Context()); items[0].Accounts != 1 {
		t.Errorf("open accounts = %d", items[0].Accounts)
	}
	var name string
	if err := f.store.DB().QueryRow(`SELECT name FROM money_accounts WHERE account_id = 'acc-card'`).Scan(&name); err != nil || name != "Example Card" {
		t.Errorf("the closed account's row is gone (%q, %v): its old transactions lose their name", name, err)
	}
}
