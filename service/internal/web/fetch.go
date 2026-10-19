package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	fetchTimeout  = 10 * time.Second
	maxPageBytes  = 2 << 20 // largest body read from a page
	maxRedirects  = 5
	maxPageRunes  = 12000 // text handed to the model
	excerptRunes  = 300
	maxParseNodes = 40000
	userAgent     = "Mozilla/5.0 (compatible; Hekate/1.0; personal assistant)"
)

// Page is what web_read extracts from a URL.
type Page struct {
	Title     string
	URL       string // final URL after redirects
	Source    string // host
	Text      string // readable text, at most maxPageRunes
	Excerpt   string
	Truncated bool // the page or its text was cut
}

// newSafeClient builds an HTTP client that only connects where guard allows.
// Proxies are ignored on purpose: a proxy would be the address dialed, hiding
// the real destination from the guard.
func newSafeClient(guard guardFunc) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: guard.control}
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			DisableKeepAlives:      true,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  fetchTimeout,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			// The connection to the new location is guarded when it is dialed;
			// here we only keep the scheme to http(s).
			return checkURL(req.URL, guard)
		},
	}
}

// checkURL validates a URL before any connection: http or https, a host, no
// embedded credentials, and (for a literal IP) an address the guard allows.
// Names are checked when they are dialed, against the address they resolve to.
func checkURL(u *url.URL, guard guardFunc) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https URLs can be read, not %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("the URL has no host")
	}
	if u.User != nil {
		return errors.New("URLs with a username or password are not allowed")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		p, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return fmt.Errorf("bad port %q", port)
		}
		if err := guard(netip.AddrPortFrom(ip, uint16(p))); err != nil {
			return err
		}
	}
	return nil
}

// Read fetches rawURL and extracts its readable text.
func (s *Service) Read(ctx context.Context, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Page{}, fmt.Errorf("that is not a valid URL: %w", err)
	}
	if err := checkURL(u, s.guard); err != nil {
		return Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,text/plain;q=0.8")
	resp, err := s.reader.Do(req)
	if err != nil {
		if errors.Is(err, errBlocked) {
			return Page{}, errBlocked
		}
		return Page{}, fmt.Errorf("could not fetch the page: %w", unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Page{}, fmt.Errorf("the site answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("could not read the page: %w", unwrapURLError(err))
	}
	truncated := len(body) > maxPageBytes
	if truncated {
		body = body[:maxPageBytes]
	}

	ct := resp.Header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	if mt == "" {
		mt = http.DetectContentType(body)
		if i := strings.IndexByte(mt, ';'); i >= 0 {
			mt = mt[:i]
		}
		ct = ""
	}
	finalURL := resp.Request.URL
	page := Page{URL: finalURL.String(), Source: strings.TrimPrefix(finalURL.Hostname(), "www."), Truncated: truncated}

	switch mt {
	case "text/html", "application/xhtml+xml":
		text, title, err := extractHTML(body, ct, finalURL)
		if err != nil {
			return Page{}, err
		}
		page.Title, page.Text = title, text
	case "text/plain":
		text, err := toUTF8(body, ct)
		if err != nil {
			return Page{}, err
		}
		// Plain text needs no parsing; cut before tidying so a huge file is cheap.
		if len(text) > 8*maxPageRunes {
			text, page.Truncated = strings.ToValidUTF8(text[:8*maxPageRunes], ""), true
		}
		page.Text = tidyText(text)
	default:
		return Page{}, fmt.Errorf("that is %s, not a web page or plain text, so I cannot read it", mtOrUnknown(mt))
	}
	if strings.TrimSpace(page.Text) == "" {
		return Page{}, errors.New("the page has no readable text (it may need JavaScript)")
	}
	page.Text, page.Truncated = clipRunes(page.Text, maxPageRunes, page.Truncated)
	page.Excerpt, _ = clipRunes(strings.Join(strings.Fields(page.Text), " "), excerptRunes, false)
	return page, nil
}

func mtOrUnknown(mt string) string {
	if mt == "" {
		return "an unknown type"
	}
	return mt
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// clipRunes cuts s to at most n runes on a word boundary where it can.
func clipRunes(s string, n int, already bool) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, already
	}
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \n"); i > n*8/10 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…", true
}

func toUTF8(body []byte, contentType string) (string, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", fmt.Errorf("could not decode the page: %w", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("could not decode the page: %w", err)
	}
	return string(b), nil
}

// extractHTML pulls the main text out of an HTML page with readability,
// falling back to all visible text for pages it cannot make sense of.
func extractHTML(body []byte, contentType string, pageURL *url.URL) (text, title string, err error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", "", fmt.Errorf("could not decode the page: %w", err)
	}
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", fmt.Errorf("could not parse the page: %w", err)
	}
	title = strings.TrimSpace(documentTitle(doc))

	p := readability.NewParser()
	p.MaxElemsToParse = maxParseNodes
	art, perr := p.ParseAndMutate(doc, pageURL)
	if perr == nil && art.Node != nil {
		var sb strings.Builder
		if err := art.RenderText(&sb); err == nil {
			if t := tidyText(sb.String()); len([]rune(t)) >= 200 {
				if at := strings.TrimSpace(art.Title()); at != "" {
					title = at
				}
				return t, title, nil
			}
		}
	}
	// Short pages and non-article pages: take everything visible. The
	// readability parse mutated doc, so parse again from the bytes.
	r2, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", "", err
	}
	doc2, err := html.Parse(r2)
	if err != nil {
		return "", "", fmt.Errorf("could not parse the page: %w", err)
	}
	return tidyText(visibleText(doc2)), title, nil
}

func documentTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" {
		var sb strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				sb.WriteString(c.Data)
			}
		}
		return strings.Join(strings.Fields(sb.String()), " ")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := documentTitle(c); t != "" {
			return t
		}
	}
	return ""
}

var skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "head": true, "iframe": true}

var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "section": true, "article": true, "blockquote": true, "pre": true,
	"ul": true, "ol": true, "table": true, "header": true, "footer": true, "main": true,
}

func visibleText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			sb.WriteString(n.Data)
		case html.ElementNode:
			if skipTags[n.Data] {
				return
			}
			if blockTags[n.Data] {
				sb.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[n.Data] {
			sb.WriteString("\n")
		}
	}
	walk(n)
	return sb.String()
}

var (
	spaceRun   = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// tidyText squeezes whitespace: single spaces within lines and at most one
// blank line between paragraphs.
func tidyText(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
