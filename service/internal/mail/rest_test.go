package mail

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/store"
)

type restFixture struct {
	*fixture
	mux *http.ServeMux
}

func newRestFixture(t *testing.T) *restFixture {
	t.Helper()
	f := newFixture(t)
	mux := http.NewServeMux()
	f.svc.Routes(mux)
	return &restFixture{f, mux}
}

func (r *restFixture) do(method, path, body string) (int, map[string]any) {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			r.t.Fatalf("%s %s: bad JSON %q", method, path, rec.Body.String())
		}
	}
	return rec.Code, out
}

func subjects(out map[string]any) []string {
	var s []string
	for _, m := range out["messages"].([]any) {
		s = append(s, m.(map[string]any)["subject"].(string))
	}
	return s
}

func TestRESTStatusSummaryAndList(t *testing.T) {
	r := newRestFixture(t)
	r.deliver("INBOX", mailSpec{subject: "Re: need your answer", from: "Mina <mina@example.org>"})
	r.deliver("INBOX", mailSpec{subject: "Urgent: card expiring"})
	r.deliver("INBOX", mailSpec{subject: "Big sale", seen: true})
	r.deliver("INBOX", mailSpec{subject: "Weekly digest"})
	r.sweep()

	code, out := r.do("GET", "/api/mail/status", "")
	accts := out["accounts"].([]any)
	if code != 200 || out["configured"] != true || len(accts) != 1 {
		t.Fatalf("status = %d %v", code, out)
	}
	a := accts[0].(map[string]any)
	if a["name"] != "personal" || a["address"] != testUser || a["ok"] != true || a["error"] != nil || a["inbox_count"] != float64(4) || a["last_sweep_at"] == nil {
		t.Errorf("account status = %v", a)
	}

	code, out = r.do("GET", "/api/mail/summary", "")
	unread := out["unread"].(map[string]any)
	if code != 200 || unread["needs_reply"] != float64(1) || unread["important"] != float64(1) || unread["newsletter"] != float64(1) || unread["promo"] != nil {
		t.Errorf("summary = %d %v (the read promo must not count)", code, out)
	}
	if att := out["attention"].([]any); len(att) != 2 {
		t.Errorf("attention = %v", att)
	}

	code, out = r.do("GET", "/api/mail/messages", "")
	if code != 200 || len(subjects(out)) != 4 || subjects(out)[0] != "Weekly digest" {
		t.Errorf("list = %d %v (newest first)", code, subjects(out))
	}
	if _, out = r.do("GET", "/api/mail/messages?category=promo", ""); len(subjects(out)) != 1 {
		t.Errorf("category filter = %v", subjects(out))
	}
	if _, out = r.do("GET", "/api/mail/messages?unread=true", ""); len(subjects(out)) != 3 {
		t.Errorf("unread filter = %v", subjects(out))
	}
	if _, out = r.do("GET", "/api/mail/messages?q=card", ""); len(subjects(out)) != 1 {
		t.Errorf("q filter = %v", subjects(out))
	}
	if _, out = r.do("GET", "/api/mail/messages?q=digest&account=personal&limit=1", ""); len(subjects(out)) != 1 {
		t.Errorf("combined filters = %v", subjects(out))
	}
	// folder=inbox is the default; archived mail shows with folder=all only.
	id := r.id("Big sale")
	if _, err := r.svc.Archive(t.Context(), []int64{id}); err != nil {
		t.Fatal(err)
	}
	if _, out = r.do("GET", "/api/mail/messages", ""); len(subjects(out)) != 3 {
		t.Errorf("inbox list = %v", subjects(out))
	}
	if _, out = r.do("GET", "/api/mail/messages?folder=all", ""); len(subjects(out)) != 4 {
		t.Errorf("all list = %v", subjects(out))
	}
	for _, bad := range []string{"folder=sent", "category=nonsense", "account=nope", "unread=maybe", "limit=0", "limit=x"} {
		if code, _ := r.do("GET", "/api/mail/messages?"+bad, ""); code != 400 {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
}

func TestRESTMessageActions(t *testing.T) {
	r := newRestFixture(t)
	r.deliver("INBOX", mailSpec{subject: "Read me", body: "The full text.", from: "Mina <mina@example.org>"})
	r.deliver("INBOX", mailSpec{subject: "Archive me"})
	r.deliver("INBOX", mailSpec{subject: "Trash me"})
	r.sweep()
	path := func(subject, suffix string) string { return "/api/mail/messages/" + r.bySubject(subject).ID + suffix }

	code, out := r.do("GET", path("Read me", ""), "")
	body := out["body"].(map[string]any)
	msg := out["message"].(map[string]any)
	if code != 200 || body["text"] != "The full text." || body["truncated"] != false || msg["subject"] != "Read me" {
		t.Errorf("read = %d %v", code, out)
	}
	if !r.bySubject("Read me").Unread {
		t.Error("reading over REST marked it read")
	}

	code, out = r.do("POST", path("Archive me", "/archive"), "")
	if code != 200 || out["folder"] != "Archive" || out["subject"] != "Archive me" {
		t.Errorf("archive = %d %v", code, out)
	}
	code, out = r.do("POST", path("Trash me", "/trash"), "")
	if code != 200 || out["folder"] != "Trash" {
		t.Errorf("trash = %d %v (the owner's own click needs no approval)", code, out)
	}
	if code, out = r.do("POST", path("Read me", "/read"), `{"unread":false}`); code != 200 || out["unread"] != false {
		t.Errorf("mark read = %d %v", code, out)
	}
	if code, out = r.do("POST", path("Read me", "/read"), `{"unread":true}`); code != 200 || out["unread"] != true {
		t.Errorf("mark unread = %d %v", code, out)
	}
	if code, _ = r.do("POST", path("Read me", "/read"), `{}`); code != 400 {
		t.Errorf("missing unread = %d", code)
	}
	if code, _ = r.do("POST", path("Read me", "/read"), `{"unread":"yes"}`); code != 400 {
		t.Errorf("bad unread = %d", code)
	}
	if code, _ = r.do("GET", "/api/mail/messages/99999", ""); code != 404 {
		t.Errorf("unknown id = %d", code)
	}
	if code, _ = r.do("POST", "/api/mail/messages/99999/archive", ""); code != 404 {
		t.Errorf("archive unknown = %d", code)
	}
	if code, _ = r.do("GET", "/api/mail/messages/abc", ""); code != 400 {
		t.Errorf("bad id = %d", code)
	}
	if got := r.changeCount(); got < 3 {
		t.Errorf("OnChange calls = %d, actions should invalidate mail", got)
	}
}

func TestRESTGoneFromServerIs404(t *testing.T) {
	r := newRestFixture(t)
	uid := r.deliver("INBOX", mailSpec{subject: "Poof"})
	r.sweep()
	id := r.bySubject("Poof").ID
	r.expungeFrom("INBOX", uid)
	if code, _ := r.do("GET", "/api/mail/messages/"+id, ""); code != 404 {
		t.Errorf("read of a deleted message = %d, want 404", code)
	}
	if code, _ := r.do("POST", "/api/mail/messages/"+id+"/archive", ""); code != 404 {
		t.Errorf("archive of a deleted message = %d, want 404", code)
	}
}

func TestRESTRules(t *testing.T) {
	r := newRestFixture(t)
	rule, err := r.svc.CreateRule(t.Context(), RuleSpec{From: "@example.org", Action: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := r.do("GET", "/api/mail/rules", "")
	rules := out["rules"].([]any)
	if code != 200 || len(rules) != 1 {
		t.Fatalf("rules = %d %v", code, out)
	}
	got := rules[0].(map[string]any)
	match := got["match"].(map[string]any)
	if got["id"] != rule.ID || match["from"] != "@example.org" || match["category"] != nil || got["action"] != "archive" ||
		got["folder"] != nil || got["label"] != "Archive mail from example.org" || got["hits"] != float64(0) || got["last_hit_at"] != nil || got["created_at"] == nil {
		t.Errorf("rule JSON = %v", got)
	}
	if code, _ := r.do("DELETE", "/api/mail/rules/"+rule.ID, ""); code != 204 {
		t.Errorf("delete = %d", code)
	}
	if code, _ := r.do("DELETE", "/api/mail/rules/"+rule.ID, ""); code != 404 {
		t.Errorf("delete again = %d", code)
	}
	if code, _ := r.do("DELETE", "/api/mail/rules/x", ""); code != 400 {
		t.Errorf("bad rule id = %d", code)
	}
}

func TestUnconfiguredRESTAndService(t *testing.T) {
	st, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Routes(mux)
	get := func(path string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := get("/api/mail/status"); code != 200 || out["configured"] != false || len(out["accounts"].([]any)) != 0 {
		t.Errorf("status = %d %v", code, out)
	}
	if code, out := get("/api/mail/messages"); code != 200 || len(out["messages"].([]any)) != 0 {
		t.Errorf("messages = %d %v", code, out)
	}
	if code, out := get("/api/mail/summary"); code != 200 || len(out["attention"].([]any)) != 0 || len(out["unread"].(map[string]any)) != 0 {
		t.Errorf("summary = %d %v", code, out)
	}
	if code, out := get("/api/mail/rules"); code != 200 || len(out["rules"].([]any)) != 0 {
		t.Errorf("rules = %d %v", code, out)
	}
	if code, _ := get("/api/mail/messages/1"); code != 404 {
		t.Errorf("message = %d", code)
	}
	svc.Run(t.Context()) // returns at once
	if text, err := svc.PromptContext(t.Context(), svc.now()); err != nil || text != "" {
		t.Errorf("prompt context = %q %v, want empty", text, err)
	}
	if _, err := svc.CreateRule(t.Context(), RuleSpec{From: "@example.org", Action: "archive"}); err == nil {
		t.Error("created a rule with no mail set up")
	}
}

func TestNewRequiresApprovalsWhenConfigured(t *testing.T) {
	st, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, err = New(Options{Store: st, Accounts: []config.MailAccount{{Name: "a", User: testUser, Pass: "x", Host: "127.0.0.1:1"}}})
	if err == nil {
		t.Error("New accepted accounts without Approvals")
	}
	if _, err := New(Options{}); err == nil {
		t.Error("New accepted a missing store")
	}
}
