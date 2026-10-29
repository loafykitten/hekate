package review

import (
	"strings"
	"testing"

	"github.com/loafykitten/hekate/service/internal/proto"
)

func TestParseReportLeniently(t *testing.T) {
	calls := []Call{{ID: 1, Command: "cat compose.yaml"}, {ID: 2, Command: "docker ps"}}
	body := `{"verdict": "needs_changes", "summary": "It has gaps.", "findings": [{"severity": "High", "title": "No login",
		"evidence_call_ids": [1, 1, 2], "evidence": "x", "fix": "y"}], "verified_ok": ["tags exist"]}`
	for name, text := range map[string]string{
		"bare":                 body,
		"a stray brace after":  body + "\n}",                                              // MiMo, in the trial
		"prose and a fence":    "I've exhausted my budget.\n\n```json\n" + body + "\n```", // DeepSeek, in the trial
		"a brace in the prose": "Checked {everything}. " + body,
	} {
		rep, err := parseReport(text, calls)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		f := rep.Findings
		if rep.Summary != "It has gaps." || len(f) != 1 || f[0].Severity != "high" ||
			strings.Join(f[0].Commands, "|") != "cat compose.yaml|docker ps" || f[0].Unverified {
			t.Errorf("%s: %+v", name, rep)
		}
	}
}

func TestParseReportChecksEvidence(t *testing.T) {
	text := `{"verdict": "looks_good", "summary": "Mostly fine.", "findings": [
		{"severity": "low", "title": "nit", "evidence_call_ids": [], "evidence": "unverified knowledge: z"},
		{"severity": "medium", "title": "made up", "evidence_call_ids": [7]},
		{"severity": "medium", "title": "real", "evidence_call_ids": [1]},
		{"severity": "urgent", "title": "odd severity", "evidence_call_ids": [1]},
		{"severity": "high", "title": "  ", "evidence_call_ids": []}]}`
	rep, err := parseReport(text, []Call{{ID: 1, Command: "ls"}})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, f := range rep.Findings {
		titles = append(titles, f.Severity+":"+f.Title)
	}
	if got := strings.Join(titles, ", "); got != "medium:real, medium:odd severity, low:nit" {
		t.Errorf("findings %s: made-up evidence and empty titles go, worst first", got)
	}
	if !rep.Findings[2].Unverified || rep.Findings[0].Unverified {
		t.Errorf("unverified marks %+v", rep.Findings)
	}
	if rep.Verdict != VerdictChanges {
		t.Errorf("verdict %q: medium findings can't look good", rep.Verdict)
	}
}

func TestParseReportRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"no JSON":    "Everything looks fine to me.",
		"no summary": `{"verdict": "looks_good", "findings": []}`,
		"broken":     `{"verdict": "looks_good", "summary": "fine"`,
	} {
		if _, err := parseReport(text, nil); err == nil {
			t.Errorf("%s: read as a report", name)
		}
	}
}

func TestFollowupNote(t *testing.T) {
	v := proto.Review{Round: 2, Summary: "Two gaps.", Findings: []proto.ReviewFinding{
		{Severity: "high", Title: "No login", Evidence: "WEB_AUTHENTICATION unset", Fix: "set it"},
		{Severity: "low", Title: "Mullvad", Unverified: true},
	}}
	note := FollowupNote(v, "Sam")
	for _, want := range []string{"Not from Sam", "Sam sees this review", "last round", "1. [high] No login",
		"Evidence: WEB_AUTHENTICATION unset", "Suggested fix: set it", "couldn't check this one", "Then tell Sam"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
}

func TestUserMessageBiggerPicture(t *testing.T) {
	msg := userMessage(Request{Round: 1, Owner: []string{"set it up"}, Replies: []string{"done"}, Actions: []string{"lab_shell: x"},
		Handover: "It runs at :8080.", Budget: Budget(3), Earlier: &Report{Verified: []string{"gluetun is healthy"},
			Findings: []Finding{{Severity: "high", Title: "Wrong password"}}}}, "rules")
	for _, want := range []string{"It runs at :8080.", "gluetun is healthy", "Wrong password", "last review of her earlier work",
		"You have 18 commands"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if Budget(0) != MinBudget || Budget(100) != MaxBudget {
		t.Errorf("budget bounds: %d %d", Budget(0), Budget(100))
	}
}
