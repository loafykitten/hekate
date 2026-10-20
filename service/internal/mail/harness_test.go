package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-msgauth/dkim"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
)

const (
	testUser = "rika@example.com"
	testPass = "app-password"
)

// fixtureClock is a settable clock.
type fixtureClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixtureClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixtureClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeApprovals stands in for the approvals service: it records requests,
// keeps the approvals table the noise report reads, and can run actions at
// once like a standing rule would.
type fakeApprovals struct {
	t     *testing.T
	store *store.Store
	clock *fixtureClock

	mu    sync.Mutex
	execs map[string]approvals.Executor
	reqs  []approvals.Request
	auto  map[string]bool // kinds that run at once, as under a standing rule
}

func (f *fakeApprovals) Register(kind string, exec approvals.Executor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execs == nil {
		f.execs = map[string]approvals.Executor{}
	}
	f.execs[kind] = exec
	return nil
}

func (f *fakeApprovals) Request(ctx context.Context, r approvals.Request) (approvals.Approval, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	auto := f.auto[r.Kind]
	exec := f.execs[r.Kind]
	f.mu.Unlock()
	now := f.clock.Now().UTC().Format(time.RFC3339)
	status := approvals.StatusPending
	var result, by *string
	if auto {
		msg, err := exec(ctx, r.Payload)
		status = approvals.StatusDone
		if err != nil {
			status, msg = approvals.StatusFailed, err.Error()
		}
		result, by = &msg, ptr(approvals.DecidedByRule)
	}
	res, err := f.store.DB().ExecContext(ctx, `INSERT INTO approvals (kind, title, detail, scope, scope_label, payload, status, result, decided_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Kind, r.Title, r.Detail, r.Scope, r.ScopeLabel, string(r.Payload), status, result, by, now, now)
	if err != nil {
		return approvals.Approval{}, err
	}
	id, _ := res.LastInsertId()
	a := approvals.Approval{ID: strconv.FormatInt(id, 10), Kind: r.Kind, Title: r.Title, Detail: r.Detail, Status: status, Result: result, DecidedBy: by, CreatedAt: now, ExpiresAt: now}
	return a, nil
}

// run executes the stored payload of a request, as the owner allowing it would.
func (f *fakeApprovals) run(ctx context.Context, i int) (string, error) {
	f.mu.Lock()
	r := f.reqs[i]
	exec := f.execs[r.Kind]
	f.mu.Unlock()
	return exec(ctx, r.Payload)
}

func (f *fakeApprovals) requests() []approvals.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]approvals.Request(nil), f.reqs...)
}

// decide records the owner's decision in the approvals table.
func (f *fakeApprovals) decide(id, status string) {
	f.t.Helper()
	_, err := f.store.DB().Exec(`UPDATE approvals SET status = ?, decided_at = ? WHERE id = ?`, status, f.clock.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		f.t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

// fakeModel is an OpenAI-style streaming endpoint that classifies by subject.
type fakeModel struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string // user prompts
	// answer overrides the reply for a prompt; nil classifies by subject.
	answer func(prompt string) (status int, text string)
}

var emailRe = regexp.MustCompile(`(?m)^\[email id=(\d+)\]\nFrom: [^\n]*\nSubject: ([^\n]*)`)

func newFakeModel(t *testing.T) *fakeModel {
	t.Helper()
	m := &fakeModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		prompt := req.Messages[len(req.Messages)-1].Content
		m.mu.Lock()
		m.reqs = append(m.reqs, prompt)
		answer := m.answer
		m.mu.Unlock()
		status, text := 200, subjectClassifier(prompt)
		if answer != nil {
			status, text = answer(prompt)
		}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"scripted failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		tx, _ := json.Marshal(text)
		for _, s := range []string{
			`{"id":"c","model":"bulk","choices":[{"index":0,"delta":{"content":` + string(tx) + `},"finish_reason":null}]}`,
			`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(w, "data: "+s+"\n\n")
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeModel) client() *llm.Client {
	return llm.New(llm.Config{BaseURL: m.srv.URL, APIKey: "test-key", MaxRetries: -1})
}

func (m *fakeModel) prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.reqs...)
}

func (m *fakeModel) setAnswer(f func(string) (int, string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answer = f
}

// subjectClassifier answers like a model would: by the subject's first words.
func subjectClassifier(prompt string) string {
	var items []string
	for _, mm := range emailRe.FindAllStringSubmatch(prompt, -1) {
		cat := "notification"
		switch s := strings.ToLower(mm[2]); {
		case strings.HasPrefix(s, "re:"), strings.Contains(s, "question"):
			cat = "needs_reply"
		case strings.Contains(s, "newsletter"), strings.Contains(s, "digest"):
			cat = "newsletter"
		case strings.Contains(s, "sale"), strings.Contains(s, "% off"):
			cat = "promo"
		case strings.Contains(s, "receipt"):
			cat = "receipt"
		case strings.Contains(s, "urgent"):
			cat = "important"
		}
		items = append(items, fmt.Sprintf(`{"id": %s, "category": %q, "gist": "About: %s"}`, mm[1], cat, truncate(mm[2], 40)))
	}
	return "```json\n[" + strings.Join(items, ",") + "]\n```"
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// fixture is a Service wired to an in-memory IMAP server, a fake model and fake approvals.
type fixture struct {
	t     *testing.T
	svc   *Service
	store *store.Store
	clock *fixtureClock
	appr  *fakeApprovals
	model *fakeModel
	user  *imapmemserver.User
	addr  string

	mu      sync.Mutex
	changes int
}

type fixtureOpts struct {
	noModel  bool
	noise    bool         // leave the weekly noise report due
	noMove   bool         // the server lacks MOVE
	stall    *atomic.Bool // while set, the server hangs on SELECT
	httpc    *http.Client
	accounts []config.MailAccount
}

func newFixture(t *testing.T, opts ...func(*fixtureOpts)) *fixture {
	t.Helper()
	var fo fixtureOpts
	for _, o := range opts {
		o(&fo)
	}
	f := &fixture{t: t, clock: &fixtureClock{now: time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)}, model: newFakeModel(t)}

	// The in-memory IMAP server: INBOX, Archive, Projects and Trash. (The
	// memory server can't flag a folder \Trash, so the name fallback is used.)
	f.user = imapmemserver.NewUser(testUser, testPass)
	for _, name := range []string{"INBOX", "Archive", "Projects", "Trash"} {
		if err := f.user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(f.user)
	caps := imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapIdle: {}}
	if fo.noMove {
		delete(caps, imap.CapMove)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			if fo.stall != nil {
				return stallingSession{mem.NewSession(), fo.stall}, nil, nil
			}
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = ln.Addr().String()
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	f.store, err = store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	if !fo.noise {
		// Most tests deliver many messages from one sender; keep the weekly report out of their way.
		if err := f.store.SetSetting(t.Context(), noiseSetting, f.clock.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	f.appr = &fakeApprovals{t: t, store: f.store, clock: f.clock, auto: map[string]bool{}}

	accounts := fo.accounts
	for i := range accounts {
		if accounts[i].Host == "" {
			accounts[i].Host = f.addr // the test server, which tests can't know yet
		}
	}
	if accounts == nil {
		accounts = []config.MailAccount{{Name: "personal", User: testUser, Pass: testPass, Host: f.addr, Archive: "Archive"}}
	}
	o := Options{
		Store: f.store, Accounts: accounts, Approvals: f.appr, OwnerName: "Rika", Location: time.UTC,
		Model: config.Model{Name: "bulk", Effort: "low"}, Now: f.clock.Now, HTTPClient: fo.httpc, LookupTXT: testLookupTXT,
		OnChange: func(string) { f.mu.Lock(); f.changes++; f.mu.Unlock() },
		Dial: func(ctx context.Context, a config.MailAccount) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", a.Host)
		},
	}
	if !fo.noModel {
		o.LLM = f.model.client()
	}
	f.svc, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.svc.close)
	return f
}

func (f *fixture) changeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changes
}

// mailSpec describes a message to deliver.
type mailSpec struct {
	from    string // "Name <addr>" or an address
	subject string
	body    string
	date    time.Time
	seen    bool
	headers string // extra header lines, each ending in \r\n
	html    bool
	msgID   string
	// dkim signs the message as this domain with the test key; dkimKeys are the
	// headers it signs (default From, Subject, List-Unsubscribe, List-Unsubscribe-Post).
	dkim     string
	dkimKeys []string
	forged   string // header lines a forger adds on top after signing, each ending in \r\n
}

// testDKIMKey signs test mail; testLookupTXT publishes it as test._domainkey
// of every domain, as a sender's DNS would.
var testDKIMKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func testLookupTXT(domain string) ([]string, error) {
	if !strings.HasPrefix(domain, "test._domainkey.") {
		return nil, &net.DNSError{Err: "no such host", Name: domain, IsNotFound: true}
	}
	pub, err := x509.MarshalPKIXPublicKey(&testDKIMKey.PublicKey)
	if err != nil {
		return nil, err
	}
	return []string{"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pub)}, nil
}

func dkimSign(raw []byte, domain string, keys []string) []byte {
	if keys == nil {
		keys = []string{"From", "Subject", "List-Unsubscribe", "List-Unsubscribe-Post"}
	}
	var b bytes.Buffer
	if err := dkim.Sign(&b, bytes.NewReader(raw), &dkim.SignOptions{Domain: domain, Selector: "test", Signer: testDKIMKey, HeaderKeys: keys}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

var specSeq int

func (m mailSpec) raw(now time.Time) []byte {
	specSeq++
	if m.from == "" {
		m.from = "Sender <sender@example.org>"
	}
	if m.date.IsZero() {
		m.date = now.Add(-time.Hour)
	}
	if m.msgID == "" {
		m.msgID = fmt.Sprintf("m%d@example.org", specSeq)
	}
	ct := "text/plain; charset=utf-8"
	if m.html {
		ct = "text/html; charset=utf-8"
	}
	body := m.body
	if body == "" {
		body = "Hello Rika, this is the body of the message."
	}
	raw := []byte("From: " + m.from + "\r\nTo: " + testUser + "\r\nSubject: " + m.subject + "\r\nDate: " + m.date.Format(time.RFC1123Z) +
		"\r\nMessage-ID: <" + m.msgID + ">\r\n" + m.headers + "MIME-Version: 1.0\r\nContent-Type: " + ct + "\r\n\r\n" + body + "\r\n")
	if m.dkim != "" {
		raw = dkimSign(raw, m.dkim, m.dkimKeys)
	}
	return append([]byte(m.forged), raw...)
}

// client opens a second IMAP connection to the server, as another mail app would.
func (f *fixture) client() *imapclient.Client {
	f.t.Helper()
	c, err := imapclient.DialInsecure(f.addr, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	return c
}

// deliver appends a message to a folder and returns its UID.
func (f *fixture) deliver(folder string, m mailSpec) imap.UID {
	f.t.Helper()
	c := f.client()
	raw := m.raw(f.clock.Now())
	arrived := f.clock.Now().Add(-time.Hour)
	if !m.date.IsZero() {
		arrived = m.date
	}
	opts := &imap.AppendOptions{Time: arrived}
	if m.seen {
		opts.Flags = []imap.Flag{imap.FlagSeen}
	}
	cmd := c.Append(folder, int64(len(raw)), opts)
	if _, err := cmd.Write(raw); err != nil {
		f.t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		f.t.Fatal(err)
	}
	data, err := cmd.Wait()
	if err != nil {
		f.t.Fatal(err)
	}
	return data.UID
}

// serverUIDs lists the UIDs in a folder.
func (f *fixture) serverUIDs(folder string) []imap.UID {
	f.t.Helper()
	c := f.client()
	sel, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		f.t.Fatal(err)
	}
	if sel.NumMessages == 0 {
		return nil
	}
	d, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		f.t.Fatal(err)
	}
	return d.AllUIDs()
}

// serverFlags returns the flags of one message.
func (f *fixture) serverFlags(folder string, uid imap.UID) []imap.Flag {
	f.t.Helper()
	c := f.client()
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		f.t.Fatal(err)
	}
	bufs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil || len(bufs) != 1 {
		f.t.Fatalf("fetch flags: %v %d", err, len(bufs))
	}
	return bufs[0].Flags
}

// expungeFrom removes a message from the server, as another client deleting it would.
func (f *fixture) expungeFrom(folder string, uid imap.UID) {
	f.t.Helper()
	c := f.client()
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		f.t.Fatal(err)
	}
	set := imap.UIDSetNum(uid)
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		f.t.Fatal(err)
	}
	if _, err := c.UIDExpunge(set).Collect(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sweep() { f.t.Helper(); f.svc.Sweep(f.t.Context()) }

// inboxMessages lists cached inbox messages, oldest first by id.
func (f *fixture) inbox() []Message {
	f.t.Helper()
	msgs, err := f.svc.Messages(f.t.Context(), Query{Limit: 200})
	if err != nil {
		f.t.Fatal(err)
	}
	// Messages returns newest first; tests read them in delivery order.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs
}

// bySubject finds a cached message (any folder, even one that left) by subject.
func (f *fixture) bySubject(subject string) Message {
	f.t.Helper()
	r, err := scanRow(f.store.DB().QueryRow(`SELECT `+rowCols+` FROM mail_messages WHERE subject = ? ORDER BY id DESC LIMIT 1`, subject))
	if err != nil {
		f.t.Fatalf("no cached message with subject %q: %v", subject, err)
	}
	return r.message()
}

func (f *fixture) id(subject string) int64 {
	n, _ := strconv.ParseInt(f.bySubject(subject).ID, 10, 64)
	return n
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// stallingSession hangs on SELECT while stall is set, like a server that stops
// answering mid-session.
type stallingSession struct {
	imapserver.Session
	stall *atomic.Bool
}

func (s stallingSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	for s.stall.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	return s.Session.Select(mailbox, options) //nolint:wrapcheck // test double
}
