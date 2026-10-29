package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// What the reviewer is shown of the thread.
const (
	ownerKept  = 6
	ownerMax   = 3000
	repliesMax = 4
	replyMax   = 5000
	actionMax  = 600
)

// Verdicts.
const (
	VerdictGood    = "looks_good"
	VerdictChanges = "needs_changes"
	VerdictBlocked = "blocked"
)

// Severities, worst first.
var severities = []string{"high", "medium", "low"}

// Limits of what a report keeps.
const (
	maxFindings = 12
	maxVerified = 15
	fieldMax    = 2000
	titleMax    = 200
)

// userMessage lays out what the reviewer reads: the owner's words, the lab's rules, what she did
// (not what she saw or thought), and what she told the owner.
func userMessage(req Request, rules string) string {
	var b strings.Builder
	b.WriteString("## What the owner asked (their messages in this conversation, oldest first)\n\n")
	for _, m := range lastN(req.Owner, ownerKept) {
		b.WriteString("> " + strings.ReplaceAll(clipText(m, ownerMax), "\n", "\n> ") + "\n\n")
	}
	b.WriteString("## The rules she must follow in her lab\n\n")
	b.WriteString(strings.TrimSpace(rules) + "\n\n")
	if h := strings.TrimSpace(req.Handover); h != "" {
		b.WriteString("## What she's handing over as done (her words)\n\n> " + strings.ReplaceAll(clipText(h, replyMax), "\n", "\n> ") + "\n\n")
	}
	b.WriteString("## What she did since the last review: the work you're reviewing (her tool calls, oldest first; their output and her reasoning are left out on purpose)\n\n")
	if len(req.Actions) == 0 {
		b.WriteString("(nothing recorded)\n")
	}
	for _, a := range req.Actions {
		b.WriteString("- " + clipText(a, actionMax) + "\n")
	}
	b.WriteString("\n## What she told the owner (oldest first, the work you're reviewing last)\n\n")
	for i, m := range lastN(req.Replies, repliesMax) {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(clipText(m, replyMax) + "\n")
	}
	if req.Earlier != nil {
		earlier, _ := json.MarshalIndent(struct {
			Verified []string  `json:"verified_ok"`
			Findings []Finding `json:"findings"`
		}{req.Earlier.Verified, req.Earlier.Findings}, "", "  ")
		b.WriteString("\n## The last review of her earlier work in this thread\n\nFor the bigger picture: what it checked and " +
			"found true, and what it found wrong (she was given those and answered them). Don't redo that work, but say so " +
			"when the new work breaks something it verified, or leaves one of its findings standing.\n\n```json\n" + string(earlier) + "\n```\n")
	}
	if req.Previous != nil {
		prev, _ := json.MarshalIndent(req.Previous.Findings, "", "  ")
		b.WriteString("\n## The first round's findings\n\nThis is the second round: she was given these and says she dealt with them. " +
			"Check each, and review what her fixes changed.\n\n```json\n" + string(prev) + "\n```\n")
	}
	fmt.Fprintf(&b, "\nReview this work. What the lab shows now is what counts. You have %d commands for it.", req.Budget)
	return b.String()
}

// wire is the report as the model writes it.
type wire struct {
	Verdict  string `json:"verdict"`
	Summary  string `json:"summary"`
	Findings []struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
		Claim    string `json:"claim"`
		IDs      []int  `json:"evidence_call_ids"`
		Evidence string `json:"evidence"`
		Fix      string `json:"fix"`
	} `json:"findings"`
	Verified []string `json:"verified_ok"`
}

// parseReport reads the model's report leniently (prose or a code fence around it, something
// after it) and checks it against the commands that ran: a finding citing a command that never
// ran is dropped; one citing none is kept, marked unverified.
func parseReport(text string, calls []Call) (Report, error) {
	w, err := decodeFirst(text)
	if err != nil {
		return Report{}, err
	}
	ran := map[int]string{}
	for _, c := range calls {
		ran[c.ID] = c.Command
	}
	rep := Report{Verdict: w.Verdict, Summary: clipText(strings.TrimSpace(w.Summary), fieldMax), Findings: []Finding{}, Verified: []string{}}
	for _, f := range w.Findings {
		title := clipText(oneLine(f.Title), titleMax)
		if title == "" {
			continue
		}
		fd := Finding{Severity: strings.ToLower(strings.TrimSpace(f.Severity)), Title: title, Claim: clipText(f.Claim, fieldMax),
			Evidence: clipText(f.Evidence, fieldMax), Fix: clipText(f.Fix, fieldMax), Commands: []string{}}
		if !slices.Contains(severities, fd.Severity) {
			fd.Severity = "medium"
		}
		cited := true
		for _, id := range f.IDs {
			cmd, ok := ran[id]
			if !ok {
				cited = false
				break
			}
			if !slices.Contains(fd.Commands, cmd) {
				fd.Commands = append(fd.Commands, cmd)
			}
		}
		if !cited {
			continue // it cites evidence that doesn't exist
		}
		fd.Unverified = len(fd.Commands) == 0
		rep.Findings = append(rep.Findings, fd)
	}
	slices.SortStableFunc(rep.Findings, func(a, b Finding) int {
		return slices.Index(severities, a.Severity) - slices.Index(severities, b.Severity)
	})
	if len(rep.Findings) > maxFindings {
		rep.Findings = rep.Findings[:maxFindings]
	}
	for _, v := range w.Verified {
		if v = clipText(oneLine(v), fieldMax); v != "" && len(rep.Verified) < maxVerified {
			rep.Verified = append(rep.Verified, v)
		}
	}
	serious := slices.ContainsFunc(rep.Findings, func(f Finding) bool { return f.Severity != "low" })
	switch {
	case !slices.Contains([]string{VerdictGood, VerdictChanges, VerdictBlocked}, rep.Verdict) && len(rep.Findings) > 0:
		rep.Verdict = VerdictChanges
	case !slices.Contains([]string{VerdictGood, VerdictChanges, VerdictBlocked}, rep.Verdict):
		rep.Verdict = VerdictGood
	case rep.Verdict == VerdictGood && serious:
		rep.Verdict = VerdictChanges // it can't look good with real problems in it
	}
	if rep.Summary == "" {
		return Report{}, errors.New("it has no summary")
	}
	return rep, nil
}

// decodeFirst reads the first JSON object in text that decodes as a report.
func decodeFirst(text string) (wire, error) {
	lastErr := errors.New("there's no JSON object in it")
	for i := strings.IndexByte(text, '{'); i >= 0; {
		var w wire
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		err := dec.Decode(&w)
		if err == nil && (w.Summary != "" || w.Verdict != "" || w.Findings != nil) {
			return w, nil
		}
		if err != nil {
			lastErr = fmt.Errorf("it isn't valid JSON: %w", err)
		}
		j := strings.IndexByte(text[i+1:], '{')
		if j < 0 {
			break
		}
		i += j + 1
	}
	return wire{}, lastErr
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clipText cuts s to at most n runes, marking the cut.
func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
