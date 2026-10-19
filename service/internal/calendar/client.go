package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/google/uuid"

	"github.com/loafykitten/hekate/service/internal/config"
)

// Errors callers can test with errors.Is.
var (
	errNotFound = errors.New("event not found")
	errConflict = errors.New("the event was changed elsewhere")
	errAuth     = errors.New("the calendar server rejected the credentials")
)

const (
	maxObjectBytes = 1 << 20 // largest calendar object we will read back
	requestTimeout = 20 * time.Second
)

// object is one stored calendar resource.
type object struct {
	path string
	etag string // raw header form, quoted, ready for If-Match
	cal  *ical.Calendar
}

// davClient talks CalDAV to one calendar collection.
type davClient struct {
	base     *url.URL
	user     string
	pass     config.Secret
	name     string
	hc       *http.Client
	cd       *caldav.Client
	log      *slog.Logger
	mu       sync.Mutex
	resolved string // cached collection path
}

func newDAVClient(rawURL, user string, pass config.Secret, name string, hc *http.Client, log *slog.Logger) (*davClient, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("calendar url: %w", err)
	}
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	cd, err := caldav.NewClient(webdav.HTTPClientWithBasicAuth(hc, user, pass.Reveal()), rawURL)
	if err != nil {
		return nil, fmt.Errorf("calendar client: %w", err)
	}
	return &davClient{base: base, user: user, pass: pass, name: name, hc: hc, cd: cd, log: log}, nil
}

func (c *davClient) abs(p string) string {
	u := *c.base
	u.Path = p
	u.RawPath = ""
	u.RawQuery = ""
	return u.String()
}

// statusError is a non-2xx answer from the server.
type statusError struct {
	op   string
	code int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("calendar server: %s returned %d %s", e.op, e.code, http.StatusText(e.code))
}

func isStatus(err error, code int) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == code
}

// isAuthErr recognises authentication failures reported by go-webdav, whose
// HTTP error type is private and renders as "401 Unauthorized".
func isAuthErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "401 Unauthorized") || strings.Contains(msg, "403 Forbidden") ||
		strings.Contains(msg, "webdav: unauthenticated")
}

// wrapErr turns library HTTP errors (which carry the status in a private
// type) into ours, keeping the message free of credentials.
func wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if isAuthErr(err) {
		return fmt.Errorf("%s: %w", op, errAuth)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// do sends a raw request with credentials and checks the status.
func (c *davClient) do(ctx context.Context, op, method, path string, body []byte, hdr map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	//nolint:gosec // G704: the URL is the operator-configured CalDAV server plus a path it returned
	req, err := http.NewRequestWithContext(ctx, method, c.abs(path), rd)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	req.SetBasicAuth(c.user, c.pass.Reveal())
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req) //nolint:gosec // G704: same operator-configured server
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: %w", op, errAuth)
	}
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &statusError{op: op, code: resp.StatusCode}
	}
	return resp, nil
}

// collection returns the path of the configured calendar, finding or creating
// it on first use.
func (c *davClient) collection(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolved != "" {
		return c.resolved, nil
	}
	home, err := c.home(ctx)
	if err != nil {
		return "", err
	}
	cals, err := c.cd.FindCalendars(ctx, home)
	if err != nil {
		return "", wrapErr("list calendars", err)
	}
	for _, cal := range cals {
		if cal.Name == c.name && supportsEvents(cal) {
			c.resolved = cal.Path
			return c.resolved, nil
		}
	}
	path, err := c.create(ctx, home, cals)
	if err != nil {
		return "", err
	}
	c.log.Info("created calendar", "name", c.name)
	c.resolved = path
	return path, nil
}

func supportsEvents(cal caldav.Calendar) bool {
	if len(cal.SupportedComponentSet) == 0 {
		return true
	}
	for _, n := range cal.SupportedComponentSet {
		if strings.EqualFold(n, ical.CompEvent) {
			return true
		}
	}
	return false
}

// home finds the calendar home set; if the server does not do principal
// discovery the configured URL itself is taken as the home.
func (c *davClient) home(ctx context.Context) (string, error) {
	principal, err := c.cd.FindCurrentUserPrincipal(ctx)
	if err != nil {
		if isAuthErr(err) {
			return "", fmt.Errorf("find principal: %w", errAuth)
		}
		c.log.Debug("calendar principal discovery failed; using the configured url as the calendar home", "err", err)
		return ensureSlash(c.base.Path), nil
	}
	home, err := c.cd.FindCalendarHomeSet(ctx, principal)
	if err != nil || home == "" {
		c.log.Debug("calendar home discovery failed; using the configured url as the calendar home", "err", err)
		return ensureSlash(c.base.Path), nil
	}
	return ensureSlash(home), nil
}

func ensureSlash(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasSuffix(p, "/") {
		return p + "/"
	}
	return p
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// create makes the calendar with MKCALENDAR, which go-webdav does not offer.
func (c *davClient) create(ctx context.Context, home string, existing []caldav.Calendar) (string, error) {
	taken := map[string]bool{}
	for _, cal := range existing {
		taken[cal.Path] = true
	}
	var xmlName bytes.Buffer
	if err := xmlEscape(&xmlName, c.name); err != nil {
		return "", err
	}
	body := []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop>` +
		`<D:displayname>` + xmlName.String() + `</D:displayname>` +
		`<C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>` +
		`</D:prop></D:set></C:mkcalendar>`)

	candidates := []string{slug(c.name), uuid.NewString()}
	var lastErr error
	for _, s := range candidates {
		if s == "" {
			continue
		}
		path := home + s + "/"
		if taken[path] {
			continue
		}
		resp, err := c.do(ctx, "create calendar", "MKCALENDAR", path, body, map[string]string{"Content-Type": "application/xml; charset=utf-8"})
		if err == nil {
			_ = resp.Body.Close()
			return path, nil
		}
		lastErr = err
		// 405 or 409: that name is in use by something else; try the next.
		if !isStatus(err, http.StatusMethodNotAllowed) && !isStatus(err, http.StatusConflict) {
			return "", err
		}
	}
	return "", fmt.Errorf("create calendar %q: %w", c.name, lastErr)
}

func xmlEscape(w *bytes.Buffer, s string) error {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	_, err := w.WriteString(r.Replace(s))
	return err
}

func quoteETag(e string) string {
	if e == "" || strings.HasPrefix(e, `"`) || strings.HasPrefix(e, "W/") {
		return e
	}
	return `"` + e + `"`
}

// query returns every object with an event overlapping [from, to).
func (c *davClient) query(ctx context.Context, from, to time.Time) ([]object, error) {
	col, err := c.collection(ctx)
	if err != nil {
		return nil, err
	}
	objs, err := c.runQuery(ctx, col, caldav.CompFilter{Name: ical.CompEvent, Start: from.UTC(), End: to.UTC()})
	if err != nil && strings.Contains(err.Error(), "404 Not Found") {
		// The calendar was deleted or moved on the server: forget the cached
		// path so the next call finds or recreates it.
		c.mu.Lock()
		c.resolved = ""
		c.mu.Unlock()
	}
	return objs, err
}

func (c *davClient) runQuery(ctx context.Context, col string, f caldav.CompFilter) ([]object, error) {
	objs, err := c.cd.QueryCalendar(ctx, col, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: ical.CompCalendar, AllProps: true, AllComps: true},
		CompFilter:  caldav.CompFilter{Name: ical.CompCalendar, Comps: []caldav.CompFilter{f}},
	})
	if err != nil {
		return nil, wrapErr("query calendar", err)
	}
	out := make([]object, 0, len(objs))
	for _, o := range objs {
		out = append(out, object{path: o.Path, etag: quoteETag(o.ETag), cal: o.Data})
	}
	return out, nil
}

var safeUID = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,200}$`)

// find returns the object holding uid.
func (c *davClient) find(ctx context.Context, uid string) (*object, error) {
	col, err := c.collection(ctx)
	if err != nil {
		return nil, err
	}
	// Objects Hekate wrote live at <collection>/<uid>.ics; try that first.
	if safeUID.MatchString(uid) {
		o, err := c.get(ctx, col+uid+".ics")
		switch {
		case err == nil:
			return o, nil
		case !errors.Is(err, errNotFound):
			return nil, err
		}
	}
	objs, err := c.runQuery(ctx, col, caldav.CompFilter{
		Name:  ical.CompEvent,
		Props: []caldav.PropFilter{{Name: ical.PropUID, TextMatch: &caldav.TextMatch{Text: uid}}},
	})
	if err != nil {
		// Some servers reject property filters; scan everything instead.
		c.log.Debug("uid filter query failed; scanning the calendar", "err", err)
		objs, err = c.runQuery(ctx, col, caldav.CompFilter{Name: ical.CompEvent})
		if err != nil {
			return nil, err
		}
	}
	for i := range objs {
		for _, ch := range objs[i].cal.Children {
			if ch.Name == ical.CompEvent && rawText(ch.Props.Get(ical.PropUID)) == uid {
				return &objs[i], nil
			}
		}
	}
	return nil, errNotFound
}

func (c *davClient) get(ctx context.Context, path string) (*object, error) {
	resp, err := c.do(ctx, "read event", http.MethodGet, path, nil, map[string]string{"Accept": ical.MIMEType})
	if err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil, errNotFound
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	cal, err := ical.NewDecoder(io.LimitReader(resp.Body, maxObjectBytes)).Decode()
	if err != nil {
		return nil, fmt.Errorf("read event: %w", err)
	}
	return &object{path: path, etag: resp.Header.Get("ETag"), cal: cal}, nil
}

// put writes cal at path. An empty etag means create only (If-None-Match: *);
// otherwise it must match the stored version (If-Match), and a mismatch is
// errConflict.
func (c *davClient) put(ctx context.Context, path string, cal *ical.Calendar, etag string) (string, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return "", fmt.Errorf("encode event: %w", err)
	}
	hdr := map[string]string{"Content-Type": "text/calendar; charset=utf-8"}
	if etag == "" {
		hdr["If-None-Match"] = "*"
	} else {
		hdr["If-Match"] = etag
	}
	resp, err := c.do(ctx, "save event", http.MethodPut, path, buf.Bytes(), hdr)
	if err != nil {
		if isStatus(err, http.StatusPreconditionFailed) {
			return "", errConflict
		}
		return "", err
	}
	_ = resp.Body.Close()
	return resp.Header.Get("ETag"), nil
}

// remove deletes path if its ETag still matches.
func (c *davClient) remove(ctx context.Context, path, etag string) error {
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-Match"] = etag
	}
	resp, err := c.do(ctx, "delete event", http.MethodDelete, path, nil, hdr)
	if err != nil {
		switch {
		case isStatus(err, http.StatusPreconditionFailed):
			return errConflict
		case isStatus(err, http.StatusNotFound):
			return errNotFound
		}
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func objectPath(collection, uid string) string {
	return collection + uid + ".ics"
}
