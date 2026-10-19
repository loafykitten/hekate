package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Run is the sweep loop: once at start, then every Interval, until ctx is
// done. It returns at once when no account is configured.
func (s *Service) Run(ctx context.Context) {
	if !s.Configured() {
		return
	}
	defer s.close()
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval):
		}
	}
}

// Sweep checks every account once: sync the inbox, classify new mail, apply
// rules, and (weekly) look for noisy senders. Errors are stored per account
// and shown in the status; they never stop the loop.
func (s *Service) Sweep(ctx context.Context) {
	changed := false
	for _, a := range s.accounts {
		if ctx.Err() != nil {
			return
		}
		ch, err := s.sweepAccount(ctx, a)
		changed = changed || ch
		if err != nil && ctx.Err() == nil {
			s.log.Warn("mail sweep failed", "account", a.cfg.Name, "error", err)
			s.saveError(ctx, a.cfg.Name, oneLine(err.Error(), 300))
			changed = true // the status changed
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err := s.pruneLeft(ctx); err != nil {
		s.log.Warn("mail cache cleanup failed", "error", err)
	}
	if n, err := s.noiseReport(ctx); err != nil {
		s.log.Warn("mail noise report failed", "error", err)
	} else if n > 0 {
		changed = true
	}
	if changed {
		s.changed()
	}
}

// sweepAccount is one account's turn. The sync holds the account's connection;
// classification calls the model between short IMAP reads, so a slow model does
// not block the owner's actions.
func (s *Service) sweepAccount(ctx context.Context, a *account) (bool, error) {
	changed, err := s.syncInbox(ctx, a)
	if err != nil {
		return changed, err
	}
	n, err := s.classifyPending(ctx, a)
	changed = changed || n > 0
	if err != nil {
		return changed, fmt.Errorf("classify: %w", err)
	}
	applied, err := s.applyRules(ctx, a)
	changed = changed || applied > 0
	if err != nil {
		return changed, fmt.Errorf("apply rules: %w", err)
	}
	return changed, nil
}

// cached is the part of a stored inbox row that sync compares with the server.
type cached struct {
	id        int64
	uid       uint32
	validity  uint32
	unread    bool
	messageID string
}

func (s *Service) cachedInbox(ctx context.Context, name string) ([]cached, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, uid, uidvalidity, unread, message_id FROM mail_messages
		WHERE account = ? AND folder = 'INBOX' AND gone = 0`, name)
	if err != nil {
		return nil, fmt.Errorf("load cached inbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []cached
	for rows.Next() {
		var c cached
		if err := rows.Scan(&c.id, &c.uid, &c.validity, &c.unread, &c.messageID); err != nil {
			return nil, fmt.Errorf("load cached inbox: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err() //nolint:wrapcheck // plain iteration error
}

// syncInbox brings the account's cached inbox in line with the server: new
// messages are cached, read state follows the server, and messages that are no
// longer in the inbox are marked as having left it. A changed UIDVALIDITY (the
// server renumbered the mailbox) re-syncs the recent mail and carries
// classifications over by Message-ID.
func (s *Service) syncInbox(ctx context.Context, a *account) (bool, error) {
	name := a.cfg.Name
	st, err := s.accountState(ctx, name)
	if err != nil {
		return false, err
	}
	changed := false
	err = s.run(ctx, a, func(c *imapclient.Client) error {
		sel, err := c.Select(inbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("open the inbox: %w", err)
		}
		validity := sel.UIDValidity
		full := st.validity == 0 || st.validity != validity
		if st.validity != 0 && full {
			s.log.Info("mail inbox was renumbered, re-syncing", "account", name)
		}

		known, err := s.cachedInbox(ctx, name)
		if err != nil {
			return err
		}

		// seen: every UID the server has that this sync looked at, with its read state.
		seen := map[uint32]bool{}
		var fetchUIDs []imap.UID
		if full {
			uids, err := searchRecent(c, s.now())
			if err != nil {
				return err
			}
			fetchUIDs = uids
		} else {
			flags, err := listFlags(c, sel.NumMessages)
			if err != nil {
				return err
			}
			for uid := range flags {
				seen[uint32(uid)] = true
				if uint32(uid) > st.lastUID {
					fetchUIDs = append(fetchUIDs, uid)
				}
			}
			for _, k := range known {
				if k.validity != validity || !seen[k.uid] {
					continue
				}
				if unread := flags[imap.UID(k.uid)]; unread != k.unread {
					if err := s.setUnread(ctx, k.id, unread); err != nil {
						return err
					}
					changed = true
				}
			}
		}

		// Stale rows (other UIDVALIDITY) that new messages may take over.
		stale := map[string]cached{}
		for _, k := range known {
			if k.validity != validity && k.messageID != "" {
				stale[k.messageID] = k
			}
		}
		slices.Sort(fetchUIDs)
		if len(fetchUIDs) > firstSyncCap {
			fetchUIDs = fetchUIDs[len(fetchUIDs)-firstSyncCap:]
		}
		touched := map[int64]bool{}
		hs, err := fetchHeaders(c, fetchUIDs, s.now())
		if err != nil {
			return err
		}
		for _, h := range hs {
			id, isNew, err := s.upsertInbox(ctx, name, validity, h, stale)
			if err != nil {
				return err
			}
			touched[id] = true
			seen[uint32(h.uid)] = true
			changed = changed || isNew
		}
		maxUID := uint32(0)
		if !full {
			maxUID = st.lastUID
		}
		for u := range seen {
			maxUID = max(maxUID, u)
		}

		// Reconcile: cached inbox rows the server no longer lists have left the inbox.
		for _, k := range known {
			if touched[k.id] {
				continue
			}
			if k.validity != validity || !seen[k.uid] {
				s.markGone(ctx, k.id)
				changed = true
			}
		}
		return s.saveSync(ctx, name, validity, maxUID)
	})
	return changed, err
}

// searchRecent finds the UIDs a first sync caches: mail from the last 30 days.
func searchRecent(c *imapclient.Client, now time.Time) ([]imap.UID, error) {
	data, err := c.UIDSearch(&imap.SearchCriteria{Since: now.AddDate(0, 0, -firstSyncDays)}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search the inbox: %w", err)
	}
	return data.AllUIDs(), nil
}

// listFlags returns every inbox UID with whether it is unread.
func listFlags(c *imapclient.Client, count uint32) (map[imap.UID]bool, error) {
	out := map[imap.UID]bool{}
	if count == 0 {
		return out, nil
	}
	var all imap.UIDSet
	all.AddRange(1, 0)
	bufs, err := c.Fetch(all, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("list the inbox: %w", err)
	}
	for _, b := range bufs {
		out[b.UID] = !slices.Contains(b.Flags, imap.FlagSeen)
	}
	return out, nil
}

// upsertInbox stores a fetched inbox message. A message already cached under
// this UID is refreshed; one that matches a stale row by Message-ID (after a
// UIDVALIDITY change) takes that row over so its id and classification survive.
func (s *Service) upsertInbox(ctx context.Context, account string, validity uint32, h fetchedHeader, stale map[string]cached) (int64, bool, error) {
	now := stamp(s.now())
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM mail_messages WHERE account = ? AND folder = 'INBOX' AND uidvalidity = ? AND uid = ?`,
		account, validity, h.uid).Scan(&id)
	switch {
	case err == nil:
		_, err = s.db.ExecContext(ctx, `UPDATE mail_messages SET unread = ?, gone = 0 WHERE id = ?`, h.unread, id)
		if err != nil {
			return 0, false, fmt.Errorf("update mail: %w", err)
		}
		return id, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, fmt.Errorf("look up mail: %w", err)
	}
	if old, ok := stale[h.messageID]; ok && h.messageID != "" {
		delete(stale, h.messageID)
		_, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET uidvalidity = ?, uid = ?, unread = ?, gone = 0, left_inbox_at = NULL WHERE id = ?`,
			validity, h.uid, h.unread, old.id)
		if err != nil {
			return 0, false, fmt.Errorf("update mail: %w", err)
		}
		return old.id, false, nil
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mail_messages
		(account, folder, uidvalidity, uid, message_id, from_name, from_addr, subject, date, unread, list_id, unsub_url, has_post, first_seen_at)
		VALUES (?, 'INBOX', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account, validity, h.uid, h.messageID, oneLine(h.fromName, 200), h.fromAddr, oneLine(h.subject, 500), stamp(h.date), h.unread,
		h.listID, h.unsubURL, h.oneClick, now)
	if err != nil {
		return 0, false, fmt.Errorf("cache mail: %w", err)
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("cache mail: %w", err)
	}
	return id, true, nil
}

// pending is a message waiting to be classified.
type pending struct {
	id       int64
	uid      uint32
	validity uint32
	fromName string
	fromAddr string
	subject  string
	listID   string
	unsub    bool
	snippet  string
}

// classifyPending classifies the account's unclassified recent inbox mail in
// batches. It returns how many messages got a category.
func (s *Service) classifyPending(ctx context.Context, a *account) (int, error) {
	if s.llm == nil || !s.llm.Configured() {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, uid, uidvalidity, from_name, from_addr, subject, list_id, unsub_url <> ''
		FROM mail_messages WHERE account = ? AND folder = 'INBOX' AND gone = 0 AND classified_at IS NULL
		AND classify_attempts < ? AND date >= ? ORDER BY date DESC LIMIT ?`,
		a.cfg.Name, classifyTries, stamp(s.now().AddDate(0, 0, -classifyDays)), classifyPerSweep)
	if err != nil {
		return 0, fmt.Errorf("find unclassified mail: %w", err)
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.uid, &p.validity, &p.fromName, &p.fromAddr, &p.subject, &p.listID, &p.unsub); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("find unclassified mail: %w", err)
		}
		todo = append(todo, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, fmt.Errorf("find unclassified mail: %w", err)
	}

	done := 0
	for batch := range slices.Chunk(todo, classifyBatch) {
		if ctx.Err() != nil {
			return done, ctx.Err() //nolint:wrapcheck // context errors are matched by callers
		}
		if err := s.snippets(ctx, a, batch); err != nil {
			return done, err
		}
		verdicts, err := s.classify(ctx, batch)
		if err != nil {
			return done, err
		}
		for _, p := range batch {
			v, ok := verdicts[p.id]
			if !ok {
				if _, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET classify_attempts = classify_attempts + 1 WHERE id = ?`, p.id); err != nil {
					return done, fmt.Errorf("update mail: %w", err)
				}
				continue
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE mail_messages SET category = ?, gist = ?, classified_at = ? WHERE id = ?`,
				v.category, v.gist, stamp(s.now()), p.id); err != nil {
				return done, fmt.Errorf("update mail: %w", err)
			}
			done++
		}
	}
	return done, nil
}

// snippets fetches the start of each message's text for the classifier. They
// live in memory for the one request and are never stored. A message that can't
// be read is classified from its headers alone.
func (s *Service) snippets(ctx context.Context, a *account, batch []pending) error {
	byValidity := map[uint32][]int{}
	for i, p := range batch {
		byValidity[p.validity] = append(byValidity[p.validity], i)
	}
	for validity, idx := range byValidity {
		uids := make([]imap.UID, len(idx))
		for j, i := range idx {
			uids[j] = imap.UID(batch[i].uid)
		}
		var raws map[imap.UID][]byte
		err := s.run(ctx, a, func(c *imapclient.Client) error {
			var err error
			raws, err = fetchRaw(c, inbox, validity, uids, 64<<10)
			return err
		})
		if errors.Is(err, ErrGone) {
			continue // renumbered under us: the next sync sorts it out
		}
		if err != nil {
			return err
		}
		for _, i := range idx {
			raw := raws[imap.UID(batch[i].uid)]
			if len(raw) == 0 {
				continue
			}
			text, err := bodyText(raw)
			if err != nil {
				continue
			}
			batch[i].snippet, _ = clipRunes(strings.Join(strings.Fields(text), " "), snippetRunes)
		}
	}
	return nil
}
