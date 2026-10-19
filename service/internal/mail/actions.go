package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Outcome says what an action did to a set of messages.
type Outcome struct {
	Done    int    // moved or changed (including ones already where they were going)
	Missing int    // no longer on the server where Hekate last saw them
	Dest    string // folder moved to, when there was one
}

// byAccount splits rows by account, keeping the order accounts first appear in.
func byAccount(rows []row) ([]string, map[string][]int64) {
	var order []string
	groups := map[string][]int64{}
	for _, r := range rows {
		if _, ok := groups[r.Account]; !ok {
			order = append(order, r.Account)
		}
		groups[r.Account] = append(groups[r.Account], r.ID)
	}
	return order, groups
}

// serverKey identifies a folder on the server: UIDs are only meaningful with
// the UIDVALIDITY they were seen under.
type serverKey struct {
	folder   string
	validity uint32
}

// groupRows splits rows by where they live. Rows that cannot be addressed
// (gone, or moved somewhere the server did not report) are counted as missing.
func groupRows(rows []row) (map[serverKey][]row, []serverKey, int) {
	groups := map[serverKey][]row{}
	var order []serverKey
	missing := 0
	for _, r := range rows {
		if r.Gone || r.UID == 0 || r.Folder == "" {
			missing++
			continue
		}
		k := serverKey{r.Folder, r.Validity}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	return groups, order, missing
}

func uidsOf(rows []row) []imap.UID {
	out := make([]imap.UID, len(rows))
	for i, r := range rows {
		out[i] = imap.UID(r.UID)
	}
	return out
}

func (s *Service) account(name string) (*account, error) {
	a := s.byName[name]
	if a == nil {
		return nil, fmt.Errorf("mail account %q is no longer configured", name)
	}
	return a, nil
}

func (s *Service) requireConfigured() error {
	if !s.Configured() {
		return ErrNotConfigured
	}
	return nil
}

// moveMessages moves messages to the folder t names, keeping their Hekate ids.
func (s *Service) moveMessages(ctx context.Context, ids []int64, t target) (Outcome, error) {
	rows, err := s.rowsByID(ctx, ids)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	order, groups := byAccount(rows)
	for _, name := range order {
		a, err := s.account(name)
		if err != nil {
			return out, err
		}
		err = s.run(ctx, a, func(c *imapclient.Client) error {
			dest, err := a.resolve(c, t)
			if err != nil {
				return err
			}
			out.Dest = dest
			// Reload under the account's lock: a sweep may have moved things on.
			fresh, err := s.rowsByID(ctx, groups[name])
			if err != nil {
				return err
			}
			byKey, keys, missing := groupRows(fresh)
			out.Missing += missing
			for _, k := range keys {
				if err := s.moveGroup(ctx, c, k, byKey[k], dest, &out); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return out, s.wrapServerErr(err)
		}
	}
	return out, nil
}

func (s *Service) moveGroup(ctx context.Context, c *imapclient.Client, k serverKey, rows []row, dest string, out *Outcome) error {
	var todo []row
	for _, r := range rows {
		if r.Folder == dest {
			out.Done++
			continue
		}
		todo = append(todo, r)
	}
	if len(todo) == 0 {
		return nil
	}
	m, err := moveUIDs(c, k.folder, k.validity, uidsOf(todo), dest)
	if errors.Is(err, ErrGone) {
		for _, r := range todo {
			s.markGone(ctx, r.ID)
		}
		out.Missing += len(todo)
		return nil
	}
	if err != nil {
		return err
	}
	for _, r := range todo {
		src := imap.UID(r.UID)
		switch {
		case m.missing[src]:
			s.markGone(ctx, r.ID)
			out.Missing++
		case m.uid[src] != 0:
			if err := s.relocate(ctx, r.ID, dest, m.validity, uint32(m.uid[src])); err != nil {
				return err
			}
			out.Done++
		case m.srcSeen[src] || len(m.srcSeen) == 0:
			// Moved, but the server did not say where (no UIDPLUS): the row keeps
			// its id and folder but can't be addressed any more.
			if err := s.relocate(ctx, r.ID, dest, m.validity, 0); err != nil {
				return err
			}
			out.Done++
		default:
			s.markGone(ctx, r.ID)
			out.Missing++
		}
	}
	return nil
}

// setRead marks messages read (unread false) or unread.
func (s *Service) setRead(ctx context.Context, ids []int64, unread bool) (Outcome, error) {
	rows, err := s.rowsByID(ctx, ids)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	order, groups := byAccount(rows)
	for _, name := range order {
		a, err := s.account(name)
		if err != nil {
			return out, err
		}
		err = s.run(ctx, a, func(c *imapclient.Client) error {
			fresh, err := s.rowsByID(ctx, groups[name])
			if err != nil {
				return err
			}
			byKey, keys, missing := groupRows(fresh)
			out.Missing += missing
			for _, k := range keys {
				rs := byKey[k]
				missing, err := setSeen(c, k.folder, k.validity, uidsOf(rs), !unread)
				if errors.Is(err, ErrGone) {
					for _, r := range rs {
						s.markGone(ctx, r.ID)
					}
					out.Missing += len(rs)
					continue
				}
				if err != nil {
					return err
				}
				for _, r := range rs {
					if missing[imap.UID(r.UID)] {
						s.markGone(ctx, r.ID)
						out.Missing++
						continue
					}
					if err := s.setUnread(ctx, r.ID, unread); err != nil {
						return err
					}
					out.Done++
				}
			}
			return nil
		})
		if err != nil {
			return out, s.wrapServerErr(err)
		}
	}
	return out, nil
}

// wrapServerErr turns a failure talking to the mail server into a 502 that does
// not leak server text; errors that already carry a status pass through.
func (s *Service) wrapServerErr(err error) error {
	var he *httpx.Error
	if errors.As(err, &he) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.log.Warn("mail server request failed", "error", err)
	return errServer()
}

// Archive moves messages to their account's archive folder.
func (s *Service) Archive(ctx context.Context, ids []int64) (Outcome, error) {
	return s.moveMessages(ctx, ids, target{kind: ActionArchive})
}

// Trash moves messages to their account's trash folder. Callers go through an
// approval unless the owner is acting directly.
func (s *Service) Trash(ctx context.Context, ids []int64) (Outcome, error) {
	return s.moveMessages(ctx, ids, target{kind: ActionTrash})
}

// MoveTo moves messages to an existing folder other than the trash.
func (s *Service) MoveTo(ctx context.Context, ids []int64, folder string) (Outcome, error) {
	if strings.TrimSpace(folder) == "" {
		return Outcome{}, httpx.BadRequest("a folder name is required")
	}
	return s.moveMessages(ctx, ids, target{kind: ActionMove, name: strings.TrimSpace(folder)})
}

// MarkRead marks messages read.
func (s *Service) MarkRead(ctx context.Context, ids []int64) (Outcome, error) {
	return s.setRead(ctx, ids, false)
}

// MarkUnread marks messages unread.
func (s *Service) MarkUnread(ctx context.Context, ids []int64) (Outcome, error) {
	return s.setRead(ctx, ids, true)
}

// perform runs a rule or bulk action by name.
func (s *Service) perform(ctx context.Context, action, folder string, ids []int64) (Outcome, error) {
	switch action {
	case ActionArchive:
		return s.Archive(ctx, ids)
	case ActionTrash:
		return s.Trash(ctx, ids)
	case ActionMove:
		return s.MoveTo(ctx, ids, folder)
	case ActionMarkRead:
		return s.MarkRead(ctx, ids)
	case actionMarkUnread:
		return s.MarkUnread(ctx, ids)
	}
	return Outcome{}, fmt.Errorf("unknown mail action %q", action)
}

// actionMarkUnread exists for bulk approvals only; rules never mark unread.
const actionMarkUnread = "mark_unread"

// Unsubscribe sends the RFC 8058 one-click request for a message: an HTTPS POST
// with List-Unsubscribe=One-Click to the https URL the sender published. It
// never sends mail and never GETs a link. The request goes through the same
// SSRF-guarded client web_read uses and does not follow redirects.
func (s *Service) Unsubscribe(ctx context.Context, id int64) error {
	r, err := s.rowByID(ctx, id)
	if err != nil {
		return err
	}
	if !r.canUnsubscribe() {
		return httpx.BadRequest("this sender does not offer one-click unsubscribe")
	}
	u, err := url.Parse(r.UnsubURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return httpx.BadRequest("the unsubscribe link is not a plain https URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return httpx.BadRequest("the unsubscribe link is not usable")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Hekate/1.0 (one-click unsubscribe)")
	resp, err := s.http.Do(req)
	if err != nil {
		return &httpx.Error{Status: http.StatusBadGateway, Msg: "the unsubscribe request failed: " + unwrapURL(err).Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpx.Error{Status: http.StatusBadGateway, Msg: fmt.Sprintf("the sender's server answered %d to the unsubscribe request", resp.StatusCode)}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET unsubscribed_at = ? WHERE id = ?`, stamp(s.now()), id); err != nil {
		return fmt.Errorf("record unsubscribe: %w", err)
	}
	s.changed()
	return nil
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
