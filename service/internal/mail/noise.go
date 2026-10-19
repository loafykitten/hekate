package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/store"
)

// The weekly noise report's thresholds.
const (
	noiseSetting  = "mail.noise_last_run" // RFC 3339 time of the last report
	noiseEvery    = 7 * 24 * time.Hour
	noiseWindow   = 30 * 24 * time.Hour
	noiseMinCount = 4                   // messages from one sender, none opened
	noiseMax      = 3                   // proposals per report
	noiseDenyMemo = 90 * 24 * time.Hour // a denied sender is not proposed again for this long
)

// freeMail lists domains that serve many unrelated people, so a domain rule
// for them would sweep up strangers. They are only ever proposed by address.
var freeMail = []string{
	"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com", "yahoo.com", "icloud.com",
	"me.com", "aol.com", "proton.me", "protonmail.com", "zoho.com", "zohomail.com", "gmx.com", "fastmail.com",
}

// noiseGroup counts one sender's recent inbox mail.
type noiseGroup struct {
	key       string
	count     int
	read      bool // any message was opened
	wanted    bool // any message was classified as something she shouldn't clean away
	addresses map[string]bool
}

func (g *noiseGroup) add(unread bool, category string, addr string) {
	g.count++
	g.read = g.read || !unread
	g.wanted = g.wanted || category == CategoryNeedsReply || category == CategoryImportant ||
		category == CategoryPersonal || category == CategoryReceipt
	g.addresses[addr] = true
}

func (g *noiseGroup) noisy() bool { return g.count >= noiseMinCount && !g.read && !g.wanted }

// noiseReport proposes archive rules for senders whose mail goes unopened. It
// runs at most weekly and returns how many proposals it raised.
func (s *Service) noiseReport(ctx context.Context) (int, error) {
	if !s.Configured() || s.appr == nil {
		return 0, nil
	}
	if v, err := s.store.GetSetting(ctx, noiseSetting); err == nil {
		if t, err := time.Parse(time.RFC3339, v); err == nil && s.now().Sub(t) < noiseEvery {
			return 0, nil
		}
	} else if !isNotFound(err) {
		return 0, err //nolint:wrapcheck // store errors already carry context
	}
	cands, err := s.noiseCandidates(ctx)
	if err != nil {
		return 0, err
	}
	proposed := 0
	for _, g := range cands {
		if proposed == noiseMax {
			break
		}
		skip, err := s.noiseSkip(ctx, g.key)
		if err != nil {
			return proposed, err
		}
		if skip {
			continue
		}
		spec := RuleSpec{From: g.key, Action: ActionArchive}
		a, err := s.appr.Request(ctx, approvals.Request{
			Kind:    KindRuleCreate,
			Title:   spec.label() + "?",
			Detail:  fmt.Sprintf("None of the %d messages from %s in the last 30 days were opened. Archive new ones automatically?", g.count, senderLabel(g.key)),
			Payload: payloadOf(spec),
			Notify:  true, // raised outside any conversation
		})
		if err != nil {
			return proposed, fmt.Errorf("propose a mail rule: %w", err)
		}
		if id, err := strconv.ParseInt(a.ID, 10, 64); err == nil {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO mail_noise_proposals (sender, approval_id, proposed_at) VALUES (?, ?, ?)`,
				g.key, id, stamp(s.now())); err != nil {
				return proposed, fmt.Errorf("remember a mail rule proposal: %w", err)
			}
		}
		proposed++
	}
	if err := s.store.SetSetting(ctx, noiseSetting, stamp(s.now())); err != nil {
		return proposed, err //nolint:wrapcheck // store errors already carry context
	}
	return proposed, nil
}

func isNotFound(err error) bool { return err != nil && errors.Is(err, store.ErrNotFound) }

// noiseCandidates groups the last 30 days of inbox mail by sender and returns
// the senders that qualify, busiest first. A domain is proposed when several of
// its addresses are noisy together; otherwise single addresses are.
func (s *Service) noiseCandidates(ctx context.Context) ([]*noiseGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT from_addr, unread, COALESCE(category, '') FROM mail_messages
		WHERE folder = 'INBOX' AND gone = 0 AND from_addr <> '' AND date >= ?`, stamp(s.now().Add(-noiseWindow)))
	if err != nil {
		return nil, fmt.Errorf("find noisy senders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byAddr := map[string]*noiseGroup{}
	byDomain := map[string]*noiseGroup{}
	group := func(m map[string]*noiseGroup, key string) *noiseGroup {
		g := m[key]
		if g == nil {
			g = &noiseGroup{key: key, addresses: map[string]bool{}}
			m[key] = g
		}
		return g
	}
	for rows.Next() {
		var addr, cat string
		var unread bool
		if err := rows.Scan(&addr, &unread, &cat); err != nil {
			return nil, fmt.Errorf("find noisy senders: %w", err)
		}
		group(byAddr, addr).add(unread, cat, addr)
		if d := domainOf(addr); d != "" && !slices.Contains(freeMail, d) {
			group(byDomain, "@"+d).add(unread, cat, addr)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find noisy senders: %w", err)
	}

	var out []*noiseGroup
	covered := map[string]bool{} // addresses a chosen domain already speaks for
	for _, g := range byDomain {
		if g.noisy() && len(g.addresses) >= 2 {
			out = append(out, g)
			for a := range g.addresses {
				covered[a] = true
			}
		}
	}
	for _, g := range byAddr {
		if g.noisy() && !covered[g.key] {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	return out, nil
}

// noiseSkip reports whether a sender should not be proposed now: a rule
// already covers it, a proposal is still waiting, or the owner said no lately.
func (s *Service) noiseSkip(ctx context.Context, key string) (bool, error) {
	rules, err := s.Rules(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range rules {
		if r.Match.From == nil {
			continue
		}
		if *r.Match.From == key || (key[0] != '@' && senderMatches(*r.Match.From, key)) {
			return true, nil
		}
	}
	var n int
	// Whether the owner decided is in the approvals table, which shares this database.
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_noise_proposals p JOIN approvals a ON a.id = p.approval_id
		WHERE p.sender = ? AND (a.status = 'pending' OR (a.status = 'denied' AND a.decided_at >= ?))`,
		key, stamp(s.now().Add(-noiseDenyMemo))).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check earlier proposals: %w", err)
	}
	return n > 0, nil
}
