package mail

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// unsubServer records requests to a fake sender's unsubscribe endpoint.
type unsubServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string // "METHOD path body content-type"
	status   int
	hitsLoc  int
}

func newUnsubServer(t *testing.T) *unsubServer {
	t.Helper()
	u := &unsubServer{status: 200}
	u.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		defer u.mu.Unlock()
		if r.URL.Path == "/landing" {
			u.hitsLoc++
		}
		u.requests = append(u.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body)+" "+r.Header.Get("Content-Type"))
		if u.status == 302 {
			http.Redirect(w, r, "/landing", http.StatusFound)
			return
		}
		w.WriteHeader(u.status)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *unsubServer) reqs() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.requests...)
}

func oneClickHeaders(url string) string {
	return "List-Unsubscribe: <mailto:leave@example.org>, <" + url + ">\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"
}

func TestOneClickUnsubscribe(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	f.deliver("INBOX", mailSpec{subject: "News", headers: oneClickHeaders(srv.URL + "/u/abc?x=1")})
	f.sweep()
	id := f.id("News")
	if !f.bySubject("News").CanUnsubscribe {
		t.Fatal("one-click link not detected")
	}
	if err := f.svc.Unsubscribe(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	reqs := srv.reqs()
	if len(reqs) != 1 || reqs[0] != "POST /u/abc?x=1 List-Unsubscribe=One-Click application/x-www-form-urlencoded" {
		t.Errorf("requests = %q", reqs)
	}
	if f.bySubject("News").CanUnsubscribe {
		t.Error("still offers unsubscribe after doing it")
	}
	if err := f.svc.Unsubscribe(t.Context(), id); err == nil {
		t.Error("unsubscribed twice")
	}
}

func TestUnsubscribeRefusals(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	// Only mailto: no https URL to POST to.
	f.deliver("INBOX", mailSpec{subject: "Mailto only", headers: "List-Unsubscribe: <mailto:leave@example.org>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"})
	// An https link without the one-click announcement is only a web page: never GET it.
	f.deliver("INBOX", mailSpec{subject: "Link only", headers: "List-Unsubscribe: <" + srv.URL + "/page>\r\n"})
	// A plain http URL is not acceptable even with the post header.
	f.deliver("INBOX", mailSpec{subject: "Plain http", headers: "List-Unsubscribe: <http://example.org/u>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"})
	f.sweep()
	for _, subj := range []string{"Mailto only", "Link only", "Plain http"} {
		if f.bySubject(subj).CanUnsubscribe {
			t.Errorf("%s: should not be unsubscribable", subj)
		}
		if err := f.svc.Unsubscribe(t.Context(), f.id(subj)); err == nil {
			t.Errorf("%s: unsubscribed", subj)
		}
	}
	if got := srv.reqs(); len(got) != 0 {
		t.Errorf("requests were sent: %q", got)
	}
}

func TestUnsubscribeServerErrorsAndRedirects(t *testing.T) {
	srv := newUnsubServer(t)
	f := newFixture(t, func(o *fixtureOpts) { o.httpc = srv.Client() })
	f.deliver("INBOX", mailSpec{subject: "Flaky", headers: oneClickHeaders(srv.URL + "/u/1")})
	f.sweep()
	id := f.id("Flaky")

	srv.status = 500
	if err := f.svc.Unsubscribe(t.Context(), id); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("500: %v", err)
	}
	if !f.bySubject("Flaky").CanUnsubscribe {
		t.Error("a failed attempt must not count as done")
	}
	// A redirect is not followed: following a POST redirect would GET a link.
	srv.status = 302
	if err := f.svc.Unsubscribe(t.Context(), id); err == nil {
		t.Error("a redirect counted as success")
	}
	if srv.hitsLoc != 0 {
		t.Error("the redirect target was fetched")
	}
}

func TestUnsubscribeRefusesPrivateAddresses(t *testing.T) {
	srv := newUnsubServer(t) // listens on 127.0.0.1
	f := newFixture(t)       // default client: the SSRF-guarded one
	f.deliver("INBOX", mailSpec{subject: "Internal", headers: oneClickHeaders(srv.URL + "/u/secret")})
	f.deliver("INBOX", mailSpec{subject: "Metadata", headers: oneClickHeaders("https://169.254.169.254/latest/meta-data")})
	f.sweep()
	for _, subj := range []string{"Internal", "Metadata"} {
		err := f.svc.Unsubscribe(t.Context(), f.id(subj))
		if err == nil || !strings.Contains(err.Error(), "private or internal") {
			t.Errorf("%s: err = %v, want the SSRF guard to refuse", subj, err)
		}
	}
	if got := srv.reqs(); len(got) != 0 {
		t.Errorf("the guarded client reached the private server: %q", got)
	}
}
