package reflection

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSavesMustCiteATurnFromTheWindow(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "I started cello lessons", "Lovely.")
	f.model.reply = `{"save": [
		{"kind": "fact", "content": "Plays the cello", "reason": "no citation"},
		{"kind": "fact", "content": "Has a creator page", "source_turns": ["t99"], "reason": "not in the window"},
		{"kind": "fact", "content": "Takes cello lessons", "source_turns": ["t1"], "reason": "said so"}
	], "diary": "d"}`
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(summary, "Saved 1,") || !strings.Contains(summary, "(2 changes skipped: no source turn from this conversation)") {
		t.Errorf("summary = %q", summary)
	}
	rows, err := f.svc.db.Query(`SELECT content, source_turn_id FROM memories`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var content, src string
		if err := rows.Scan(&content, &src); err != nil {
			t.Fatal(err)
		}
		n++
		if content != "Takes cello lessons" || src != "t1" {
			t.Errorf("saved %q from %q", content, src)
		}
	}
	if n != 1 {
		t.Errorf("%d memories saved, want 1", n)
	}
}

func TestUpdatesThatRewriteNeedATurnButMergesAndTuningDoNot(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a := f.remember("fact", "Has a side income")
	b := f.remember("fact", "Likes tea")
	c := f.remember("fact", "Likes oolong")
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "hi", "hello")
	f.model.reply = `{"update": [
		{"id": ` + itoa(a) + `, "content": "The side income ended", "reason": "no citation"},
		{"id": ` + itoa(b) + `, "content": "Likes tea, oolong most", "merged_from": [` + itoa(c) + `], "reason": "merge"},
		{"id": ` + itoa(b) + `, "importance": 4, "reason": "tuning"}
	], "diary": "d"}`
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(summary, "Saved 0, updated 2 ") || !strings.Contains(summary, "(1 changes skipped") {
		t.Errorf("summary = %q", summary)
	}
	if m, _ := f.mem.Get(ctx, a); m.Content != "Has a side income" {
		t.Errorf("an uncited rewrite was applied: %q", m.Content)
	}
}

func TestReflectionPromptCarriesProvenance(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.mem.SetProfile(ctx, "pronouns", "she/her"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.SetProfile(ctx, "preferred_name", "Rika"); err != nil {
		t.Fatal(err)
	}
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "My side income ended", "Understood.")
	f.model.reply = `{"diary": "d"}`
	if _, err := f.svc.Reflect(ctx); err != nil {
		t.Fatal(err)
	}
	req := f.model.asked[0]
	sys, in := strings.Join(strings.Fields(req.Messages[0].Content), " "), req.Messages[1].Content
	for _, want := range []string{
		"Facts about Rika come from what they tell you", // the chat rules' own words
		"Save only what Rika said or confirmed",
		"Never save an inference, a guess or a likely explanation", "source_turns",
		"mail rules, approvals", "an income that ended is not continuing income", "pronouns exactly",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, want := range []string{"[turn t1, ", "Who Rika is", "pronouns: she/her", "preferred_name: Rika"} {
		if !strings.Contains(in, want) {
			t.Errorf("input lacks %q", want)
		}
	}
	if strings.Index(in, "Who Rika is") > strings.Index(in, "## Conversation") {
		t.Error("identity should come before the conversation")
	}
}

func TestToolEvidenceIsLabelled(t *testing.T) {
	f := newFixture(t)
	f.say(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc), "check my mail", "Nothing urgent.")
	if _, err := f.svc.db.Exec(`INSERT INTO activity (turn_id, tool, effect, status, result, created_at) VALUES ('t1', 'mail_search', 'read', 'ok', 'aid announcement', ?)`,
		stamp(time.Date(2026, 10, 19, 12, 0, 0, 0, f.loc))); err != nil {
		t.Fatal(err)
	}
	f.model.reply = `{"diary": "d"}`
	if _, err := f.svc.Reflect(t.Context()); err != nil {
		t.Fatal(err)
	}
	in := f.model.asked[0].Messages[1].Content
	if !strings.Contains(in, "tool mail_search (ok) returned: aid announcement") || !strings.Contains(in, "What your tools returned (not things anyone said)") {
		t.Errorf("input:\n%s", in)
	}
}

func TestRunsAfterTheDayRollsOver(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if runAt.Hour != 6 || runAt.Minute != 30 {
		t.Errorf("runAt = %v", runAt)
	}
	next, ok := f.svc.Job().Next(ctx, time.Date(2026, 10, 20, 2, 0, 0, 0, f.loc), time.Time{})
	if !ok || next.In(f.loc).Format("2006-01-02 15:04") != "2026-10-20 06:30" {
		t.Errorf("next = %v %v", next, ok)
	}
	// A conversation that runs past 03:30 stays in one diary day.
	f.now = time.Date(2026, 10, 20, 6, 30, 0, 0, f.loc)
	f.say(time.Date(2026, 10, 19, 22, 0, 0, 0, f.loc), "late one", "yes")
	f.say(time.Date(2026, 10, 20, 4, 15, 0, 0, f.loc), "still up", "I see")
	f.model.reply = `{"diary": "d"}`
	summary, err := f.svc.Reflect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "wrote the diary for 2026-10-19") {
		t.Errorf("summary = %q", summary)
	}
	if in := f.model.asked[0].Messages[1].Content; !strings.Contains(in, "still up") || !strings.Contains(in, "late one") {
		t.Errorf("the night was split:\n%s", in)
	}
}
