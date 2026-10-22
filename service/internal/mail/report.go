package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/web"
)

// storedBudget is how many characters a stored (history) tool result may take
// in all: headlines, markers and message lines. History replays tool results
// clipped to 1500 characters, so everything must fit under that.
const storedBudget = 1400

// minPartLines is the least room a part's message lines get in history before
// its ids are left out altogether.
const minPartLines = 160

// folderLabel is a folder as the model reads it.
func folderLabel(folder string) string {
	switch folder {
	case "":
		return "other"
	case inbox:
		return "inbox"
	}
	return oneLine(folder, 30)
}

// idLine is the compact line that names one message: "id N · sender · subject · folder".
// Everything but the id comes from the mail, so it only ever appears inside
// untrusted markers.
func idLine(m Message, senderMax, subjectMax int) string {
	sender := m.From.Name
	if sender == "" {
		sender = m.From.Address
	}
	return fmt.Sprintf("id %s · %s · %s · %s", m.ID, oneLine(sender, senderMax), oneLine(m.Subject, subjectMax), folderLabel(m.Folder))
}

func rowLines(rows []row, senderMax, subjectMax int) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = idLine(r.message(), senderMax, subjectMax)
	}
	return out
}

func messageLines(msgs []Message, senderMax, subjectMax int) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = idLine(m, senderMax, subjectMax)
	}
	return out
}

// fenceCost is what fence adds around its lines, and the newline before it.
func fenceCost() int { return len(fence(nil)) + 1 }

// fence puts lines between nonce-delimited untrusted markers.
func fence(lines []string) string {
	id := web.Nonce()
	return fmt.Sprintf("<untrusted_mail boundary=%q>\n%s\n</untrusted_mail boundary=%q>", id, strings.Join(lines, "\n"), id)
}

// compactFence is fence for history: as many lines as fit the budget (counting
// the note on how many were left out, when some are), never more.
func compactFence(lines []string, budget int) string {
	const noteRoom = 64
	used, n := 0, 0
	for i, l := range lines {
		limit := budget
		if i < len(lines)-1 {
			limit -= noteRoom // more may follow: leave room to say so
		}
		if used+len(l)+1 > limit {
			break
		}
		used += len(l) + 1
		n++
	}
	shown := lines[:n]
	if n < len(lines) {
		shown = append(append([]string{}, shown...), fmt.Sprintf("… and %d more not kept here; search again for their ids", len(lines)-n))
	}
	return fence(shown)
}

// part is one piece of an action's report: a headline and the messages it is about.
type part struct {
	head   string // what happened, short enough for history
	detail string // extra words only the current turn needs
	rows   []row
	first  bool // kept first in history (a pending approval), so clipping never loses it
}

// report joins parts into the result text for now and the form kept in history.
// The result lists every message of every part as a compact id line; history
// keeps the headlines and as many id lines as fit storedBudget, pending
// approvals first.
func report(parts []part) (content, stored string) {
	var c []string
	for _, p := range parts {
		line := p.head
		if p.detail != "" {
			line += " " + p.detail
		}
		if len(p.rows) == 0 {
			c = append(c, line)
			continue
		}
		c = append(c, line+"\n"+fence(rowLines(p.rows, 40, 80)))
	}
	c = append(c, untrustedNote)
	return strings.Join(c, "\n"), storedReport(parts)
}

func storedReport(parts []part) string {
	const noIDs = " (ids not kept here; search again for them)"
	ordered := slices.Clone(parts)
	slices.SortStableFunc(ordered, func(a, b part) int {
		switch {
		case a.first == b.first:
			return 0
		case a.first:
			return -1
		}
		return 1
	})
	// Every headline fits, each marked as having no ids; a part that gets
	// room for its id lines trades that mark for them.
	room, left := storedBudget, 0
	for _, p := range ordered {
		room -= len(p.head) + 1
		if len(p.rows) > 0 {
			room -= len(noIDs)
			left++
		}
	}
	markers := fenceCost()
	st := make([]string, 0, len(ordered))
	for _, p := range ordered {
		if len(p.rows) == 0 {
			st = append(st, p.head)
			continue
		}
		avail := room + len(noIDs)
		share := avail / left // a fair share; what a part doesn't use goes to the rest
		if p.first {
			share = avail // a pending approval's ids matter most
		}
		left--
		if share-markers < minPartLines {
			st = append(st, p.head+noIDs)
			continue
		}
		block := "\n" + compactFence(rowLines(p.rows, 22, 38), share-markers)
		room -= len(block) - len(noIDs)
		st = append(st, p.head+block)
	}
	return strings.Join(st, "\n")
}

// askHead files an approval request like ask and also returns a short headline
// for history. what, when given, says what is asked ("trashing these 4").
func (s *Service) askHead(ctx context.Context, r approvals.Request, what string) (agent.Result, string, error) {
	a, err := s.appr.Request(ctx, r)
	if err != nil {
		return agent.Result{}, "", fmt.Errorf("ask for approval: %w", err)
	}
	res := agent.Result{Card: approvalCard(a)}
	result := ""
	if a.Result != nil {
		result = *a.Result
	}
	byRule := a.DecidedBy != nil && *a.DecidedBy == approvals.DecidedByRule
	var head string
	switch {
	case a.Status == approvals.StatusDone && byRule:
		head = "Done (standing rule): " + result
		res.Content = head
	case a.Status == approvals.StatusFailed:
		head = "It was allowed but failed: " + result + ". Tell the user."
		res.Content = head
	default:
		to := "to approve"
		if what != "" {
			to += " " + what
		}
		head = fmt.Sprintf("Asked %s %s (approval #%s).", s.owner, to, a.ID)
		res.Content = fmt.Sprintf("Asked %s %s (approval #%s); it will run when they allow it. Do not do it another way meanwhile. "+
			"This only says you asked: on later turns, Approvals in your prompt says whether it is still waiting.", s.owner, to, a.ID)
	}
	return res, head, nil
}

// ruleCovers reports whether a standing "always allow" for kind covers mail
// from addr: its address, or a domain it belongs to.
func (s *Service) ruleCovers(ctx context.Context, kind, addr string) bool {
	if addr == "" {
		return false
	}
	for _, scope := range append([]string{"from:" + addr}, withinScopes(addr)...) {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM approval_rules WHERE kind = ? AND scope = ?`, kind, scope).Scan(&one)
		if err == nil {
			return true
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false
		}
	}
	return false
}

// categoryNote says plainly which categories a sender rule with no category
// of its own will still act on, or "" when that doesn't apply.
func (sp RuleSpec) categoryNote() string {
	if sp.From == "" || sp.Category != "" || (sp.Action != ActionTrash && sp.Action != ActionArchive) {
		return ""
	}
	spared := map[string]bool{}
	for _, e := range sp.Except {
		if e.Category != "" && e.From == "" && e.Subject == "" {
			spared[e.Category] = true
		}
	}
	var acts []string
	for _, c := range categories {
		if !spared[c] {
			acts = append(acts, strings.ReplaceAll(c, "_", " "))
		}
	}
	acts = append(acts, "mail not sorted yet")
	verb := map[string]string{ActionTrash: "trash", ActionArchive: "archive"}[sp.Action]
	note := "Heads-up: this rule has no category, so it will still " + verb + " mail from " + strings.TrimPrefix(sp.From, "@") +
		" classified as: " + strings.Join(acts, ", ") + "."
	if !spared["important"] || !spared["needs_reply"] {
		note += " That can include important or needs-reply mail such as security or fraud alerts."
	}
	note += " To spare a sender's transactional mail, match a category like promo or newsletter instead of excepting categories."
	return note
}
