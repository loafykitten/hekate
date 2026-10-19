package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/httpx"
)

// account is one IMAP mailbox and its connection. mu allows one operation at a
// time: the sweep, the owner's clicks and the model's tools queue behind each
// other, so a sync never sees a half-finished move.
type account struct {
	cfg config.MailAccount

	mu        sync.Mutex
	conn      *imapclient.Client
	nc        net.Conn
	lastUsed  time.Time
	mailboxes []*imap.ListData // LIST result for this connection, nil until asked
}

// dialTLS opens the production connection: implicit TLS to the account's host.
func dialTLS(ctx context.Context, a config.MailAccount) (net.Conn, error) {
	host, _, err := net.SplitHostPort(a.Host)
	if err != nil {
		return nil, fmt.Errorf("mail host %q: %w", a.Host, err)
	}
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: dialTimeout},
		Config:    &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
	}
	return d.DialContext(ctx, "tcp", a.Host)
}

// run calls fn with a connected, authenticated client while holding the
// account. Every network operation inside fn is bounded: a watchdog closes
// the connection when the call runs past opTimeout or ctx ends. A failure that
// is not a clean server refusal drops the connection, so the next call
// reconnects.
func (s *Service) run(ctx context.Context, a *account, fn func(c *imapclient.Client) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err //nolint:wrapcheck // context errors are matched by callers
	}
	if err := s.ensure(ctx, a); err != nil {
		return err
	}
	// The client sets and clears its own read deadlines, so a server that
	// stalls between responses can't be bounded by a deadline: the watchdog
	// closes the connection instead.
	release := watchdog(ctx, a.nc, s.opTimeout)
	err := fn(a.conn)
	if release() {
		s.drop(a)
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // context errors are matched by callers
		}
		return errTimeout
	}
	if err != nil {
		// A refusal from the server, or a problem with what was asked for,
		// leaves the connection fine; anything else may have broken it.
		var imapErr *imap.Error
		var asked *httpx.Error
		if !errors.As(err, &imapErr) && !errors.As(err, &asked) {
			s.drop(a)
		}
		return err
	}
	a.lastUsed = s.now()
	return nil
}

// watchdog closes nc when ctx ends or d passes, whichever is first, until the
// returned release is called. release reports whether it closed the
// connection, and waits for a close already under way, so nothing touches the
// connection after release returns.
func watchdog(ctx context.Context, nc net.Conn, d time.Duration) (release func() (fired bool)) {
	var (
		once sync.Once
		shut atomic.Bool
	)
	kill := func() {
		once.Do(func() {
			shut.Store(true)
			_ = nc.Close()
		})
	}
	timer := time.AfterFunc(d, kill)
	stop := context.AfterFunc(ctx, kill)
	return func() bool {
		timer.Stop()
		stop()
		once.Do(func() {}) // blocks until a kill in progress has finished, and disarms later ones
		return shut.Load()
	}
}

// ensure connects and signs in when there is no usable connection. A
// connection that has sat idle is probed first, because servers close idle
// sessions without telling us.
func (s *Service) ensure(ctx context.Context, a *account) error {
	if a.conn != nil {
		if s.now().Sub(a.lastUsed) < idleCheck {
			return nil
		}
		release := watchdog(ctx, a.nc, probeTimeout)
		err := a.conn.Noop().Wait()
		if !release() && err == nil {
			return nil
		}
		s.drop(a)
	}
	nc, err := s.dial(ctx, a.cfg)
	if err != nil {
		return fmt.Errorf("connect to the mail server: %w", err)
	}
	release := watchdog(ctx, nc, loginTimeout)
	c := imapclient.New(nc, nil)
	err = c.Login(a.cfg.User, a.cfg.Pass.Reveal()).Wait()
	if release() {
		_ = c.Close()
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // context errors are matched by callers
		}
		return errTimeout
	}
	if err != nil {
		_ = c.Close()
		return fmt.Errorf("sign in to the mail server: %w", err)
	}
	a.conn, a.nc, a.lastUsed, a.mailboxes = c, nc, s.now(), nil
	return nil
}

// drop closes the connection, if any.
func (s *Service) drop(a *account) {
	if a.conn != nil {
		_ = a.conn.Close()
	}
	a.conn, a.nc, a.mailboxes = nil, nil, nil
}

// close drops every connection. Safe to call once the sweep has stopped.
func (s *Service) close() {
	for _, a := range s.accounts {
		a.mu.Lock()
		s.drop(a)
		a.mu.Unlock()
	}
}

// folders lists the account's mailboxes, cached for the life of the connection.
func (a *account) folders(c *imapclient.Client) ([]*imap.ListData, error) {
	if a.mailboxes != nil {
		return a.mailboxes, nil
	}
	opts := &imap.ListOptions{}
	if c.Caps().Has(imap.CapSpecialUse) {
		opts.ReturnSpecialUse = true
	}
	list, err := c.List("", "*", opts).Collect()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	a.mailboxes = list
	return list, nil
}

// trashNames are the names tried when a server does not flag its trash folder.
var trashNames = []string{"trash", "deleted items", "deleted messages", "bin"}

// pickTrash finds the trash folder: the one flagged \Trash, else a well-known name.
func pickTrash(list []*imap.ListData) (string, bool) {
	for _, l := range list {
		if slices.Contains(l.Attrs, imap.MailboxAttrTrash) && selectable(l) {
			return l.Mailbox, true
		}
	}
	for _, want := range trashNames {
		for _, l := range list {
			if strings.EqualFold(l.Mailbox, want) && selectable(l) {
				return l.Mailbox, true
			}
		}
	}
	return "", false
}

func selectable(l *imap.ListData) bool {
	return !slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) && !slices.Contains(l.Attrs, imap.MailboxAttrNonExistent)
}

// findFolder matches name against the account's folders, ignoring case.
func findFolder(list []*imap.ListData, name string) (string, bool) {
	if strings.EqualFold(name, inbox) {
		return inbox, true
	}
	for _, l := range list {
		if l.Mailbox == name && selectable(l) {
			return l.Mailbox, true
		}
	}
	for _, l := range list {
		if strings.EqualFold(l.Mailbox, name) && selectable(l) {
			return l.Mailbox, true
		}
	}
	return "", false
}

// folderNames lists selectable folders for error messages the model can act on.
func folderNames(list []*imap.ListData) string {
	var names []string
	for _, l := range list {
		if selectable(l) {
			names = append(names, l.Mailbox)
		}
	}
	return strings.Join(names, ", ")
}

// target says where messages are going.
type target struct {
	kind string // ActionArchive, ActionTrash or ActionMove
	name string // the folder to move to (kind move)
}

// pick narrows the messages an action touches, decided under the account's
// lock against their current state; nil keeps them all.
type pick func(row) bool

// unruledInbox keeps messages still waiting in the inbox for the rules: a rule
// must not act on mail the owner has filed or handled since it was selected.
func unruledInbox(r row) bool { return r.Folder == inbox && !r.Gone && !r.RulesApplied }

func (p pick) filter(rows []row) []row {
	if p == nil {
		return rows
	}
	var out []row
	for _, r := range rows {
		if p(r) {
			out = append(out, r)
		}
	}
	return out
}

// resolve finds the folder t names, creating the archive folder when the
// server does not have it yet. The cached folder list is refreshed once before
// giving up, because folders can be created while the connection is open.
func (a *account) resolve(c *imapclient.Client, t target) (string, error) {
	cached := a.mailboxes != nil
	name, err := a.lookup(c, t)
	if err != nil && cached {
		a.mailboxes = nil
		name, err = a.lookup(c, t)
	}
	return name, err
}

func (a *account) lookup(c *imapclient.Client, t target) (string, error) {
	list, err := a.folders(c)
	if err != nil {
		return "", err
	}
	switch t.kind {
	case ActionTrash:
		name, ok := pickTrash(list)
		if !ok {
			return "", httpx.BadRequest("account %q has no trash folder", a.cfg.Name)
		}
		return name, nil
	case ActionArchive:
		if name, ok := findFolder(list, a.cfg.Archive); ok {
			return name, nil
		}
		if err := c.Create(a.cfg.Archive, nil).Wait(); err != nil {
			return "", fmt.Errorf("create the %q folder: %w", a.cfg.Archive, err)
		}
		a.mailboxes = nil
		return a.cfg.Archive, nil
	default:
		name, ok := findFolder(list, t.name)
		if !ok {
			return "", httpx.BadRequest("account %q has no folder named %q (folders: %s)", a.cfg.Name, t.name, folderNames(list))
		}
		if trash, ok := pickTrash(list); ok && name == trash {
			return "", httpx.BadRequest("that is the trash folder; trashing needs the owner's approval, so use mail_trash")
		}
		return name, nil
	}
}

// selectFolder opens folder (read-only for syncing and reading) and checks its
// UIDVALIDITY still matches what the cache was built against.
func selectFolder(c *imapclient.Client, folder string, validity uint32, readOnly bool) error {
	data, err := c.Select(folder, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return fmt.Errorf("open %s: %w", folder, err)
	}
	if data.UIDValidity != validity {
		return ErrGone
	}
	return nil
}

// moved is where a message went.
type moved struct {
	validity uint32
	uid      map[imap.UID]imap.UID // source UID -> destination UID; missing when the server did not say
	srcSeen  map[imap.UID]bool     // source UIDs the server confirms it moved
	missing  map[imap.UID]bool     // source UIDs that were no longer in the folder
}

// moveUIDs moves messages of folder to dest with MOVE. The COPYUID answer
// (UIDPLUS) says where each one landed. Messages that are no longer in the
// folder are reported in missing rather than moved: a server answers MOVE of
// a vanished UID with a bare OK, which must not be mistaken for success.
func moveUIDs(c *imapclient.Client, folder string, validity uint32, uids []imap.UID, dest string) (m moved, err error) {
	if err := selectFolder(c, folder, validity, false); err != nil {
		return moved{}, err
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(uids...)}}, nil).Wait()
	if err != nil {
		return moved{}, fmt.Errorf("check messages: %w", err)
	}
	present := map[imap.UID]bool{}
	for _, u := range found.AllUIDs() {
		present[u] = true
	}
	var live []imap.UID
	for _, u := range uids {
		if present[u] {
			live = append(live, u)
		}
	}
	m = moved{uid: map[imap.UID]imap.UID{}, srcSeen: map[imap.UID]bool{}, missing: map[imap.UID]bool{}}
	for _, u := range uids {
		if !present[u] {
			m.missing[u] = true
		}
	}
	if len(live) == 0 {
		return m, nil
	}
	// Without MOVE the client falls back to COPY + STORE \Deleted + EXPUNGE,
	// which can lose mail when the copy fails and expunges anything else
	// already flagged deleted. Refuse instead.
	if !c.Caps().Has(imap.CapMove) {
		return moved{}, errNoMove
	}
	data, err := c.Move(imap.UIDSetNum(live...), dest).Wait()
	if err != nil {
		return moved{}, fmt.Errorf("move to %s: %w", dest, err)
	}
	m.validity = data.UIDValidity
	src, ok1 := numsOf(data.SourceUIDs)
	dst, ok2 := numsOf(data.DestUIDs)
	if ok1 {
		for _, u := range src {
			m.srcSeen[u] = true
		}
	}
	if ok1 && ok2 && len(src) == len(dst) {
		for i := range src {
			m.uid[src[i]] = dst[i]
		}
	}
	return m, nil
}

func numsOf(set imap.NumSet) ([]imap.UID, bool) {
	us, ok := set.(imap.UIDSet)
	if !ok {
		return nil, false
	}
	return us.Nums()
}

// setSeen marks messages read or unread.
//
// It returns the UIDs the server no longer has: STORE on a missing UID
// succeeds silently, so they are looked up first.
func setSeen(c *imapclient.Client, folder string, validity uint32, uids []imap.UID, seen bool) (missing map[imap.UID]bool, err error) {
	if err := selectFolder(c, folder, validity, false); err != nil {
		return nil, err
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(uids...)}}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("check messages: %w", err)
	}
	present := map[imap.UID]bool{}
	for _, u := range found.AllUIDs() {
		present[u] = true
	}
	missing = map[imap.UID]bool{}
	var live []imap.UID
	for _, u := range uids {
		if present[u] {
			live = append(live, u)
		} else {
			missing[u] = true
		}
	}
	if len(live) == 0 {
		return missing, nil
	}
	op := imap.StoreFlagsDel
	if seen {
		op = imap.StoreFlagsAdd
	}
	flags := &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
	if err := c.Store(imap.UIDSetNum(live...), flags, nil).Close(); err != nil {
		return nil, fmt.Errorf("change read state: %w", err)
	}
	return missing, nil
}

// header fields wanted for every message, beside the envelope.
var listHeaders = []string{"List-Unsubscribe", "List-Unsubscribe-Post", "List-Id"}

// fetchedHeader is one message's envelope, flags and list headers.
type fetchedHeader struct {
	uid       imap.UID
	messageID string
	fromName  string
	fromAddr  string
	subject   string
	date      time.Time
	unread    bool
	listID    string
	unsubURL  string
	oneClick  bool
}

// fetchHeaders reads headers for uids with BODY.PEEK, so nothing is marked read.
func fetchHeaders(c *imapclient.Client, uids []imap.UID, fallback time.Time) ([]fetchedHeader, error) {
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: listHeaders, Peek: true}
	opts := &imap.FetchOptions{UID: true, Envelope: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section}}
	var out []fetchedHeader
	for chunk := range slices.Chunk(uids, 100) {
		bufs, err := c.Fetch(imap.UIDSetNum(chunk...), opts).Collect()
		if err != nil {
			return nil, fmt.Errorf("fetch headers: %w", err)
		}
		for _, b := range bufs {
			h := fetchedHeader{uid: b.UID, unread: !slices.Contains(b.Flags, imap.FlagSeen), date: b.InternalDate}
			if e := b.Envelope; e != nil {
				h.messageID = strings.Trim(strings.TrimSpace(e.MessageID), "<>")
				h.subject = e.Subject
				if len(e.From) > 0 {
					h.fromName, h.fromAddr = displayName(e.From[0].Name), strings.ToLower(e.From[0].Addr())
				}
				// The Date header is the sender's claim; fall back to the arrival
				// time when it is missing or in the future.
				if !e.Date.IsZero() && e.Date.Before(fallback.Add(24*time.Hour)) {
					h.date = e.Date
				}
			}
			if h.date.IsZero() {
				h.date = fallback
			}
			applyListHeaders(&h, b.FindBodySection(section))
			out = append(out, h)
		}
	}
	return out, nil
}

// applyListHeaders reads List-Id and the one-click unsubscribe URL (RFC 2369,
// RFC 8058) from a header block. Only an https URL counts, and only when the
// sender also announced one-click POST.
func applyListHeaders(h *fetchedHeader, raw []byte) {
	if len(raw) == 0 {
		return
	}
	hdr, _ := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw))).ReadMIMEHeader()
	h.listID = oneLine(hdr.Get("List-Id"), 200)
	for _, part := range strings.Split(hdr.Get("List-Unsubscribe"), ",") {
		part = strings.Trim(strings.TrimSpace(part), "<>")
		if strings.HasPrefix(strings.ToLower(part), "https://") {
			h.unsubURL = part
			break
		}
	}
	h.oneClick = strings.Contains(strings.ToLower(hdr.Get("List-Unsubscribe-Post")), "list-unsubscribe=one-click")
}

// fetchRaw reads up to limit bytes of messages with BODY.PEEK. The result is
// keyed by UID; messages the server no longer has are missing.
func fetchRaw(c *imapclient.Client, folder string, validity uint32, uids []imap.UID, limit int64) (map[imap.UID][]byte, error) {
	if err := selectFolder(c, folder, validity, true); err != nil {
		return nil, err
	}
	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: limit}}
	bufs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message: %w", err)
	}
	out := make(map[imap.UID][]byte, len(bufs))
	for _, b := range bufs {
		out[b.UID] = b.FindBodySection(section)
	}
	return out, nil
}

// displayName tidies a sender's display name. Some servers (Zoho among them)
// hand back the name still in its header quotes, e.g. `"Boots from Example"`.
func displayName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = strings.ReplaceAll(name[1:len(name)-1], `\"`, `"`)
	}
	return strings.TrimSpace(name)
}
