package web

import (
	"io"
	"net/http"
	"net/netip"
	"net/url"

	"golang.org/x/net/html"
)

// Guard decides whether a connection to addr may be made. PublicOnly is the
// production guard; tests pass their own to reach a loopback server.
type Guard = func(addr netip.AddrPort) error

// PublicOnly allows only public unicast addresses.
func PublicOnly(addr netip.AddrPort) error { return publicOnly(addr) }

// NewSafeClient is the client web_read uses: it connects only where guard
// allows (nil means PublicOnly), judged against the resolved IP at dial time,
// and ignores proxies. Other packages that must call URLs found in untrusted
// text, such as mail's one-click unsubscribe, use it so the SSRF protections
// are the same everywhere.
func NewSafeClient(guard Guard) *http.Client {
	if guard == nil {
		guard = PublicOnly
	}
	return newSafeClient(guard)
}

// CheckURL rejects URLs a safe client would refuse before any connection:
// non-http(s) schemes, missing hosts, embedded credentials and literal IPs the
// guard blocks (nil means PublicOnly).
func CheckURL(u *url.URL, guard Guard) error {
	if guard == nil {
		guard = PublicOnly
	}
	return checkURL(u, guard)
}

// HTMLText flattens an HTML document to its visible text, with paragraphs on
// their own lines. Unlike web_read it keeps everything (mail is not an
// article), and it never executes or fetches anything.
func HTMLText(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err //nolint:wrapcheck // the caller adds context
	}
	return tidyText(visibleText(doc)), nil
}

// Nonce returns a short random token for the delimiters around untrusted
// text, so the text cannot fake the end of its own wrapper.
func Nonce() string { return nonce() }
