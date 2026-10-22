package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
)

func TestSystemPrompt(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	// 23:30 UTC on Sunday is already Monday morning in Berlin (UTC+2 in October).
	now := time.Date(2026, 10, 18, 23, 30, 0, 0, time.UTC)
	in := PromptInput{Tamer: "Rika", Gestures: []string{"waving", "no"}, Now: now, Location: berlin}
	got := SystemPrompt(in)

	for _, want := range []string{
		"Rika is your Tamer",
		"Available: {waving}, {no}.",
		"think_deeper",
		"[chuckles]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
		t.Error("unfilled placeholder")
	}
	if strings.Index(got, "You are Renamon") > strings.Index(got, "## How you work") {
		t.Error("order must be persona, rules")
	}
	// The clock lives in its own message, so the system prompt never changes.
	if strings.Contains(got, "## Now") {
		t.Error("the system prompt holds the clock")
	}
	if n := NowPrompt(in); n != "## Now\n\nIt is Monday, 19 October 2026, 01:30 (Europe/Berlin)." {
		t.Errorf("now = %q", n)
	}
}

// The prompt is laid out for the provider's prefix cache: the same system
// prompt every turn, then the history, then what changes (the clock and
// context) right before the current input.
func TestBuildMessagesPutsWhatChangesLast(t *testing.T) {
	h := hist(5)
	build := func(now time.Time, ctx string) []llm.Message {
		return BuildMessages(PromptInput{Tamer: "Rika", Now: now, Location: time.UTC, History: h,
			Context: []ContextBlock{{Name: "Calendar", Text: ctx}}})
	}
	a := build(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC), "nothing today")
	b := build(time.Date(2026, 10, 19, 9, 1, 0, 0, time.UTC), "lunch at noon")
	var roles []string
	for _, m := range a {
		roles = append(roles, m.Role)
	}
	if !slices.Equal(roles, []string{"system", "user", "assistant", "user", "assistant", "system", "user"}) {
		t.Fatalf("roles = %v", roles)
	}
	if !slices.Equal(contents(a[:5]), contents(b[:5])) {
		t.Error("everything before the clock must be the same from one minute to the next")
	}
	if a[5].Content != "## Now\n\nIt is Monday, 19 October 2026, 09:00 (UTC).\n\n### Calendar\n\nnothing today" {
		t.Errorf("now = %q", a[5].Content)
	}
	if a[6].Content != "m4" {
		t.Errorf("last = %q, want the current input", a[6].Content)
	}
}

func TestSystemPromptWithoutGestures(t *testing.T) {
	got := SystemPrompt(PromptInput{Tamer: "", Now: time.Now(), Location: time.UTC})
	if !strings.Contains(got, "Available: none.") || !strings.Contains(got, "No gesture animations are available") {
		t.Error("empty gesture list must be called out")
	}
	if !strings.Contains(got, "Tamer is your Tamer") {
		t.Error("default tamer name missing")
	}
}

func hist(n int) []store.Message {
	var out []store.Message
	for i := range n {
		role := store.RoleUser
		if i%2 == 1 {
			role = store.RoleAssistant
		}
		out = append(out, store.Message{ID: int64(i + 1), Role: role, Content: "m" + strconv.Itoa(i)})
	}
	return out
}

func contents(ms []llm.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Content)
	}
	return out
}

func TestHistoryWindowCutsToTheLowLimits(t *testing.T) {
	got, anchor := historyWindow(hist(41), 0)
	// The last 20 of 41 start at index 21, an assistant message, so it is dropped to begin at a user.
	if c := contents(BuildMessages(PromptInput{Now: time.Now(), Location: time.UTC, History: got})[1:]); len(got) != 19 ||
		got[0].Content != "m22" || got[len(got)-1].Content != "m40" || anchor != got[0].ID || c[len(c)-1] != "m40" {
		t.Errorf("window = %v, anchor %d", contents(BuildMessages(PromptInput{History: got})), anchor)
	}

	h := hist(10)
	for i := range h {
		h[i].Content = strings.Repeat("x", 6000)
	}
	got, _ = historyWindow(h, 0)
	if historyChars(got) > historyLowChars || got[len(got)-1].ID != h[9].ID || got[0].Role != store.RoleUser {
		t.Errorf("chars %d, messages %d", historyChars(got), len(got))
	}
	// A single input over the budget is still sent.
	if got, _ := historyWindow(h[9:], 0); len(got) != 1 {
		t.Errorf("lone input = %d messages", len(got))
	}
	if got, anchor := historyWindow(nil, 7); got != nil || anchor != 0 {
		t.Errorf("empty = %v %d", got, anchor)
	}
}

// From its anchor the window only grows, so each turn's prompt starts with the
// last one's; past the high limits it is cut back once and grows again.
func TestHistoryWindowGrowsFromItsAnchor(t *testing.T) {
	all := hist(101)
	var anchor int64
	var cuts []int
	for n := 1; n <= len(all); n += 2 { // a turn adds the user's message and her reply
		h := all[max(n-historyMaxMessages, 0):n]
		before := anchor
		var w []store.Message
		w, anchor = historyWindow(h, anchor)
		if w[len(w)-1].ID != int64(n) || w[0].Role != store.RoleUser || len(w) > historyMaxMessages {
			t.Fatalf("turn %d: window %d..%d", n, w[0].ID, w[len(w)-1].ID)
		}
		if anchor != before {
			cuts = append(cuts, n)
			if before != 0 && len(w) > historyLowMessages {
				t.Errorf("turn %d: cut to %d messages", n, len(w))
			}
		}
	}
	// The first turn anchors; then cuts come only when the window passes 40.
	if len(cuts) < 2 || len(cuts) > 5 || cuts[0] != 1 {
		t.Errorf("cuts at %v", cuts)
	}
	// An anchor that's gone (or too far back to load) starts a new window.
	if w, a := historyWindow(all[60:80], 12); a != w[0].ID || a == 12 {
		t.Errorf("stale anchor kept: %d", a)
	}
}

func TestHistoryKeepsToolExchangeAndClipsResults(t *testing.T) {
	calls := encodeToolCalls([]llm.ToolCall{{ID: "c1", Name: "lookup", Arguments: `{"q":"x"}`}})
	h := []store.Message{
		{ID: 1, Role: store.RoleTool, Content: "orphan result", ToolCallID: "gone"},
		{ID: 2, Role: store.RoleUser, Content: "q"},
		{ID: 3, Role: store.RoleAssistant, Content: "", ToolCalls: calls},
		{ID: 4, Role: store.RoleTool, Content: strings.Repeat("é", 3000), ToolCallID: "c1"},
		{ID: 5, Role: store.RoleAssistant, Content: "answer"},
		{ID: 6, Role: store.RoleUser, Content: "next"},
	}
	w, _ := historyWindow(h, 0)
	msgs := BuildMessages(PromptInput{Now: time.Now(), Location: time.UTC, History: w})[1:]
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if !slices.Equal(roles, []string{"user", "assistant", "tool", "assistant", "system", "user"}) {
		t.Fatalf("roles = %v", roles)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0] != (llm.ToolCall{ID: "c1", Name: "lookup", Arguments: `{"q":"x"}`}) || msgs[2].ToolCallID != "c1" {
		t.Errorf("tool exchange = %+v %+v", msgs[1], msgs[2])
	}
	if n := len([]rune(msgs[2].Content)); n != historyToolChars+1 {
		t.Errorf("tool result runes = %d", n)
	}
}

func TestGestureNamesFromManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := newGestureSource(dir)
	if _, err := src.Names(); err == nil {
		t.Error("missing manifest should be an error")
	}
	write(map[string]any{"animations": []map[string]any{
		{"name": "idle", "category": "idle"}, {"name": "waving", "category": "gesture"},
		{"name": "talking", "category": "talk"}, {"name": "no", "category": "gesture"},
	}})
	got, err := src.Names()
	if err != nil || !slices.Equal(got, []string{"waving", "talking", "no"}) {
		t.Fatalf("names = %v, %v", got, err)
	}
	// Changing the file is picked up (size differs, so the cache is stale).
	write(map[string]any{"animations": []map[string]any{{"name": "excited", "category": "gesture"}, {"name": "x", "category": "gesture"}, {"name": "idle", "category": "idle"}}})
	if got, _ := src.Names(); !slices.Equal(got, []string{"excited", "x"}) {
		t.Errorf("after change: %v", got)
	}
	write(map[string]any{"animations": []map[string]any{{"name": "idle", "category": "idle"}}})
	if got, err := src.Names(); err != nil || len(got) != 0 {
		t.Errorf("no gestures: %v %v", got, err)
	}
	if got, err := newGestureSource("").Names(); got != nil || err != nil {
		t.Errorf("no dir: %v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Names(); err == nil {
		t.Error("broken manifest should be an error")
	}
}

func TestRegistry(t *testing.T) {
	ok := func() Tool {
		return Tool{Name: "a", Schema: json.RawMessage(`{"type":"object"}`), Effect: EffectRead,
			Run: ThinkDeeperTool().Run}
	}
	r, err := NewRegistry(ok(), ThinkDeeperTool())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := r.Get("a")
	if a.Timeout != DefaultToolTimeout || a.Label == "" {
		t.Errorf("defaults not applied: %+v", a)
	}
	if got := r.Specs(); len(got) != 2 || got[0].Name != "a" || got[1].Name != ThinkDeeper {
		t.Errorf("specs = %+v", got)
	}
	if got := r.Specs(ThinkDeeper); len(got) != 1 {
		t.Errorf("specs except = %+v", got)
	}
	for name, tool := range map[string]Tool{
		"no name":    {Schema: json.RawMessage(`{}`), Effect: EffectRead, Run: ok().Run},
		"no run":     {Name: "b", Schema: json.RawMessage(`{}`), Effect: EffectRead},
		"bad effect": {Name: "b", Schema: json.RawMessage(`{}`), Effect: "wild", Run: ok().Run},
		"bad schema": {Name: "b", Schema: json.RawMessage(`{`), Effect: EffectRead, Run: ok().Run},
		"duplicate":  ok(),
	} {
		if err := r.Register(tool); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCollectMessages(t *testing.T) {
	segs := []segRecord{
		{0, 0, "{waving} Hi there.", "Hi there."},
		{1, 0, "Checking now.", "Checking now."},
		{2, 1, "All done.", "All done."},
	}
	calls := []llm.ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}}
	rounds := func() []*roundRec {
		return []*roundRec{
			{index: 0, model: "m", calls: calls, results: map[string]string{"c1": "res"}},
			{index: 1, model: "m", results: map[string]string{}},
		}
	}
	roles := func(ms []store.Message) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Role+":"+m.Text)
		}
		return out
	}

	msgs, text := collectMessages("t1", rounds(), segs, 2)
	if got := roles(msgs); !slices.Equal(got, []string{"assistant:Hi there. Checking now.", "tool:", "assistant:All done."}) || text != "Hi there. Checking now. All done." {
		t.Errorf("full = %v / %q", got, text)
	}
	if msgs[0].Content != "{waving} Hi there. Checking now." || msgs[0].ToolCalls == "" || msgs[1].ToolCallID != "c1" || msgs[1].Content != "res" {
		t.Errorf("full = %+v", msgs)
	}

	msgs, text = collectMessages("t1", rounds(), segs, 0)
	if got := roles(msgs); !slices.Equal(got, []string{"assistant:Hi there.", "tool:"}) || text != "Hi there." {
		t.Errorf("heard 0 = %v / %q", got, text)
	}

	msgs, text = collectMessages("t1", rounds(), segs, -1)
	if got := roles(msgs); !slices.Equal(got, []string{"assistant:", "tool:"}) || text != "" {
		t.Errorf("heard none keeps the tool exchange: %v / %q", got, text)
	}

	incomplete := rounds()
	incomplete[0].results = map[string]string{}
	msgs, _ = collectMessages("t1", incomplete, segs, 2)
	if len(msgs) != 0 {
		t.Errorf("an unanswered tool call must not be stored: %+v", msgs)
	}
}

func TestRulesKeepActionsHonestAndCoverHealthAndWeb(t *testing.T) {
	got := SystemPrompt(PromptInput{Tamer: "Rika", Gestures: []string{"waving"}, Now: time.Now()})
	for _, want := range []string{
		"only when a tool result in this turn says so",
		"Never promise to check back",
		"Never create a duplicate",
		"## Health",
		"Don't say a medication, dose or spacing is fine",
		"## Web and technical answers",
		"A forum post or comment is a report",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestRulesCoverMemoryAndLocalTimes(t *testing.T) {
	got := SystemPrompt(PromptInput{Tamer: "Rika", Gestures: []string{"waving"}, Now: time.Now()})
	for _, want := range []string{
		"local time unless a tool says otherwise",
		"## Memory",
		`"What you know about Rika"`,
		"Only say \"I'll remember that\" after the save succeeded.",
		"follow_up_at",
		"memory_forget",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Index(got, "## Memory") > strings.Index(got, "## Performing") {
		t.Error("Performing must stay the last rules section: the no-gesture note is appended to it")
	}
}
