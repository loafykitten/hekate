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
	got := SystemPrompt(PromptInput{Tamer: "Rika", Gestures: []string{"waving", "no"}, Now: now, Location: berlin})

	for _, want := range []string{
		"Rika is your Tamer",
		"Available: {waving}, {no}.",
		"## Now\n\nIt is Monday, 19 October 2026, 01:30 (Europe/Berlin).",
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
	if strings.Index(got, "You are Renamon") > strings.Index(got, "## How you work") || strings.Index(got, "## How you work") > strings.Index(got, "## Now") {
		t.Error("order must be persona, rules, now")
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

func TestHistoryCountTrim(t *testing.T) {
	msgs := BuildMessages(PromptInput{Now: time.Now(), Location: time.UTC, History: hist(41)})
	if msgs[0].Role != llm.RoleSystem {
		t.Fatal("first must be the system prompt")
	}
	got := contents(msgs[1:])
	// The last 30 of 41 start at index 11, an assistant message, so it is dropped to begin at a user.
	if len(got) != 29 || got[0] != "m12" || got[len(got)-1] != "m40" {
		t.Errorf("history = %v", got)
	}
	if msgs[1].Role != llm.RoleUser {
		t.Errorf("history must start with a user message, got %s", msgs[1].Role)
	}
}

func TestHistoryBudgetTrim(t *testing.T) {
	h := hist(10)
	for i := range h {
		h[i].Content = strings.Repeat("x", 6000)
	}
	msgs := BuildMessages(PromptInput{Now: time.Now(), Location: time.UTC, History: h})
	total := 0
	for _, m := range msgs[1:] {
		total += len(m.Content)
	}
	if total > historyBudgetChars || msgs[len(msgs)-1].Content != h[9].Content {
		t.Errorf("total %d, messages %d", total, len(msgs)-1)
	}
	if msgs[1].Role != llm.RoleUser {
		t.Error("must start at a user message")
	}
}

func TestHistoryKeepsToolExchangeAndClipsResults(t *testing.T) {
	calls := encodeToolCalls([]llm.ToolCall{{ID: "c1", Name: "lookup", Arguments: `{"q":"x"}`}})
	h := []store.Message{
		{Role: store.RoleTool, Content: "orphan result", ToolCallID: "gone"},
		{Role: store.RoleUser, Content: "q"},
		{Role: store.RoleAssistant, Content: "", ToolCalls: calls},
		{Role: store.RoleTool, Content: strings.Repeat("é", 3000), ToolCallID: "c1"},
		{Role: store.RoleAssistant, Content: "answer"},
		{Role: store.RoleUser, Content: "next"},
	}
	msgs := BuildMessages(PromptInput{Now: time.Now(), Location: time.UTC, History: h})[1:]
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if !slices.Equal(roles, []string{"user", "assistant", "tool", "assistant", "user"}) {
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
