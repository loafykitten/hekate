package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

const articleHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>The Fox Spirit - Example Times</title>
<meta property="og:site_name" content="Example Times">
<script>window.tracker = "IGNORE-THIS-SCRIPT-TEXT";</script>
<style>.x { color: red }</style></head>
<body>
<nav><ul><li><a href="/">Home</a></li><li><a href="/subscribe">Subscribe to our newsletter</a></li></ul></nav>
<article>
<h1>The Fox Spirit</h1>
<p class="byline">By A. Writer</p>
<p>The kitsune is a fox spirit from Japanese folklore. Over centuries of life it is said to gain
additional tails, with nine being the most powerful and wise of all of them.</p>
<p>Stories describe kitsune taking human form, often a beautiful woman or an old man. Some tales
treat them as tricksters, while others show them as faithful guardians of rice and the harvest.</p>
<p>Shrines to Inari, the deity of rice, are often guarded by stone fox statues. Visitors leave
offerings of fried tofu, which the foxes are fond of, according to the old stories.</p>
<p>Modern books, films and games keep the legend alive, borrowing the nine tails and the shape
changing for characters of many kinds across the decades since the first retellings.</p>
</article>
<aside class="ad">Buy now! Limited offer! SPONSORED-BLOCK</aside>
<footer>Copyright Example Times. All rights reserved.</footer>
</body></html>`

// fixtureClient makes s read from the given httptest servers, which listen on
// loopback, while still blocking every other address.
func fixtureClient(s *Service, servers ...*httptest.Server) {
	allowed := map[netip.AddrPort]bool{}
	for _, srv := range servers {
		u, _ := url.Parse(srv.URL)
		allowed[netip.MustParseAddrPort(u.Host)] = true
	}
	s.guard = func(a netip.AddrPort) error {
		if allowed[a] {
			return nil
		}
		return publicOnly(a)
	}
	s.reader = newSafeClient(s.guard)
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func htmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
}

func TestReadExtractsArticle(t *testing.T) {
	srv := serve(t, htmlHandler(articleHTML))
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL+"/story")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "The Fox Spirit" && p.Title != "The Fox Spirit - Example Times" {
		t.Errorf("title = %q", p.Title)
	}
	for _, want := range []string{"The kitsune is a fox spirit", "fried tofu", "nine tails"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text is missing %q:\n%s", want, p.Text)
		}
	}
	for _, bad := range []string{"IGNORE-THIS-SCRIPT-TEXT", "color: red", "SPONSORED-BLOCK", "Subscribe to our newsletter"} {
		if strings.Contains(p.Text, bad) {
			t.Errorf("text contains clutter %q", bad)
		}
	}
	if !strings.HasPrefix(p.URL, srv.URL) || p.Source != "127.0.0.1" {
		t.Errorf("url/source = %q %q", p.URL, p.Source)
	}
	if !strings.HasPrefix(p.Excerpt, "The kitsune") && !strings.Contains(p.Excerpt, "kitsune") {
		t.Errorf("excerpt = %q", p.Excerpt)
	}
	if len([]rune(p.Excerpt)) > excerptRunes+1 {
		t.Errorf("excerpt too long: %d", len([]rune(p.Excerpt)))
	}
}

func TestReadFallsBackForShortPages(t *testing.T) {
	srv := serve(t, htmlHandler(`<html><head><title>Hi</title></head><body><h1>Status</h1><p>All systems normal.</p><script>x()</script></body></html>`))
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "All systems normal.") || strings.Contains(p.Text, "x()") || p.Title != "Hi" {
		t.Errorf("page = %+v", p)
	}
}

func TestReadDecodesLegacyCharset(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		// "café crème" in ISO-8859-1
		_, _ = w.Write([]byte("<html><body><p>caf\xe9 cr\xe8me</p></body></html>"))
	})
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil || !strings.Contains(p.Text, "café crème") {
		t.Fatalf("page = %+v, %v", p, err)
	}
}

func TestReadPlainText(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "line one\n\n\n\nline   two\r\n")
	})
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil || p.Text != "line one\n\nline two" {
		t.Fatalf("page = %+v, %v", p, err)
	}
}

func TestReadRejectsOtherContentTypes(t *testing.T) {
	for _, ct := range []string{"image/png", "application/pdf", "application/json", "application/octet-stream", "video/mp4"} {
		t.Run(ct, func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				fmt.Fprint(w, "<html><body>looks like html but is declared otherwise</body></html>")
			})
			s := New(Options{})
			fixtureClient(s, srv)
			_, err := s.Read(context.Background(), srv.URL)
			if err == nil || !strings.Contains(err.Error(), "not a web page") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestReadSniffsMissingContentType(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Type"] = nil // suppress sniffing by net/http
		fmt.Fprint(w, "<!doctype html><html><body><p>sniffed fine</p></body></html>")
	})
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil || !strings.Contains(p.Text, "sniffed fine") {
		t.Fatalf("page = %+v, %v", p, err)
	}
}

func TestReadSizeCap(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := strings.Repeat("0123456789abcdef ", 1024) // 16 KiB
		for range 400 {                                    // ~6.4 MiB offered
			if _, err := fmt.Fprint(w, chunk); err != nil {
				return
			}
		}
	})
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated {
		t.Error("not marked truncated")
	}
	if n := len([]rune(p.Text)); n > maxPageRunes+1 {
		t.Errorf("text has %d runes, cap is %d", n, maxPageRunes)
	}
}

func TestReadStopsAtTheByteCap(t *testing.T) {
	// A page whose real content sits past 2 MiB is not read that far.
	pad := strings.Repeat("x ", maxPageBytes/2+10)
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, pad+"\nTHE-END-MARKER")
	})
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil || !p.Truncated || strings.Contains(p.Text, "THE-END-MARKER") {
		t.Fatalf("truncated=%v err=%v", p.Truncated, err)
	}
}

func TestReadTruncatesTextForTheModel(t *testing.T) {
	para := "<p>" + strings.Repeat("Sentence number one about foxes and their long histories. ", 6) + "</p>\n"
	srv := serve(t, htmlHandler("<html><body><article>"+strings.Repeat(para, 120)+"</article></body></html>"))
	s := New(Options{})
	fixtureClient(s, srv)
	p, err := s.Read(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.Text)); n > maxPageRunes+1 || n < maxPageRunes*8/10 || !p.Truncated {
		t.Errorf("runes=%d truncated=%v", n, p.Truncated)
	}
}

func TestReadErrors(t *testing.T) {
	notFound := serve(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	empty := serve(t, htmlHandler("<html><body><script>app()</script></body></html>"))
	s := New(Options{})
	fixtureClient(s, notFound, empty)
	tests := []struct {
		name, url, want string
	}{
		{"404", notFound.URL, "404"},
		{"no text", empty.URL, "no readable text"},
		{"ftp", "ftp://example.com/file", "only http and https"},
		{"file", "file:///etc/passwd", "only http and https"},
		{"no scheme", "example.com/page", "only http and https"},
		{"no host", "http:///path", "no host"},
		{"credentials", "http://user:pw@example.com/", "username or password"},
		{"garbage", "http://exa mple.com/", "not a valid URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Read(context.Background(), tt.url)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// With the production guard, the loopback test server is itself off limits.
func TestReadRefusesPrivateTargets(t *testing.T) {
	srv := serve(t, htmlHandler("<html><body><p>secret</p></body></html>"))
	s := New(Options{}) // real guard
	u, _ := url.Parse(srv.URL)
	for _, target := range []string{
		srv.URL,
		"http://localhost:" + u.Port() + "/",
		"http://[::1]:" + u.Port() + "/",
		"http://[::ffff:127.0.0.1]:" + u.Port() + "/",
		"http://127.1:" + u.Port() + "/",
		"http://2130706433:" + u.Port() + "/", // 127.0.0.1 as one number
		"http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.100.100.100/",
		"http://192.168.1.1/",
		"http://0.0.0.0:" + u.Port() + "/",
	} {
		_, err := s.Read(context.Background(), target)
		if !errors.Is(err, errBlocked) && (err == nil || !strings.Contains(err.Error(), "could not fetch")) {
			t.Errorf("%s: err = %v", target, err)
		}
		if err == nil {
			t.Errorf("%s was fetched", target)
		}
	}
}

func TestReadChecksEveryRedirect(t *testing.T) {
	private := serve(t, htmlHandler("<html><body><p>internal only</p></body></html>"))
	public := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, private.URL+"/", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/to-localhost-name":
			u, _ := url.Parse(private.URL)
			http.Redirect(w, r, "http://localhost:"+u.Port()+"/", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/ok-hop":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			htmlHandler("<html><head><title>Landed</title></head><body><p>arrived safely</p></body></html>")(w, r)
		}
	})
	s := New(Options{})
	fixtureClient(s, public) // only "public" is allowed; "private" is not

	for _, path := range []string{"/to-private", "/to-metadata", "/to-localhost-name"} {
		_, err := s.Read(context.Background(), public.URL+path)
		if !errors.Is(err, errBlocked) {
			t.Errorf("%s: err = %v, want blocked", path, err)
		}
	}
	if _, err := s.Read(context.Background(), public.URL+"/to-file"); err == nil || !strings.Contains(err.Error(), "only http and https") {
		t.Errorf("to-file: err = %v", err)
	}
	if _, err := s.Read(context.Background(), public.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("loop: err = %v", err)
	}
	p, err := s.Read(context.Background(), public.URL+"/ok-hop")
	if err != nil || !strings.Contains(p.Text, "arrived safely") || !strings.HasSuffix(p.URL, "/final") {
		t.Fatalf("good redirect: %+v, %v", p, err)
	}
}
