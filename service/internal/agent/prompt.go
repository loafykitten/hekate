package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/persona"
)

// History limits: the last messages, trimmed to a character budget (a rough
// proxy for tokens that avoids shipping a tokenizer).
const (
	historyMessages    = 30
	historyBudgetChars = 24000
	historyToolChars   = 1500 // tool results are long-lived noise; keep the head
)

// PromptInput is everything that goes into one turn's prompt.
type PromptInput struct {
	Tamer    string
	Gestures []string
	Now      time.Time // converted to Location for display
	Location *time.Location
	History  []store.Message // oldest first; may include the current user message
}

// BuildMessages assembles the system prompt (persona, rules, the clock) and
// the recent history as chat messages. The caller appends nothing for the
// current input: it is the last history message.
func BuildMessages(in PromptInput) []llm.Message {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: SystemPrompt(in)}}
	return append(msgs, historyMessagesFor(in.History)...)
}

// SystemPrompt renders persona + rules + now.
func SystemPrompt(in PromptInput) string {
	tamer := strings.TrimSpace(in.Tamer)
	if tamer == "" {
		tamer = "Tamer"
	}
	var list string
	if len(in.Gestures) == 0 {
		list = "none"
	} else {
		parts := make([]string, len(in.Gestures))
		for i, g := range in.Gestures {
			parts[i] = "{" + g + "}"
		}
		list = strings.Join(parts, ", ")
	}
	r := strings.NewReplacer("{{tamer}}", tamer, "{{gestures}}", list)

	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Replace(persona.Renamon)))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(r.Replace(persona.Rules)))
	if len(in.Gestures) == 0 {
		b.WriteString("\n- No gesture animations are available right now, so do not use any curly-brace tags at all.")
	}
	b.WriteString("\n\n")
	b.WriteString(nowBlock(in.Now, in.Location))
	return b.String()
}

func nowBlock(now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	return fmt.Sprintf("## Now\n\nIt is %s, %s (%s).",
		local.Format("Monday, 2 January 2006"), local.Format("15:04"), loc.String())
}

// historyMessagesFor converts stored messages to chat messages, trimmed to the
// message count and character budget, always starting at a user message so no
// tool result is left without its call.
func historyMessagesFor(hist []store.Message) []llm.Message {
	if len(hist) > historyMessages {
		hist = hist[len(hist)-historyMessages:]
	}
	size := func(m store.Message) int { return utf8.RuneCountInString(m.Content) + len(m.ToolCalls) }
	total := 0
	for _, m := range hist {
		total += size(m)
	}
	// Drop the oldest messages while over budget, but never the last one.
	for len(hist) > 1 && total > historyBudgetChars {
		total -= size(hist[0])
		hist = hist[1:]
	}
	for len(hist) > 1 && hist[0].Role != store.RoleUser {
		hist = hist[1:]
	}

	out := make([]llm.Message, 0, len(hist))
	for _, m := range hist {
		out = append(out, toLLM(m))
	}
	return out
}

func toLLM(m store.Message) llm.Message {
	switch m.Role {
	case store.RoleTool:
		return llm.Message{Role: llm.RoleTool, ToolCallID: m.ToolCallID, Content: clipRunes(m.Content, historyToolChars)}
	case store.RoleAssistant:
		return llm.Message{Role: llm.RoleAssistant, Content: m.Content, ToolCalls: decodeToolCalls(m.ToolCalls)}
	}
	return llm.Message{Role: llm.RoleUser, Content: m.Content}
}

type storedCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func encodeToolCalls(calls []llm.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	out := make([]storedCall, len(calls))
	for i, c := range calls {
		out[i] = storedCall{c.ID, c.Name, c.Arguments}
	}
	b, _ := json.Marshal(out) // plain strings cannot fail to marshal
	return string(b)
}

func decodeToolCalls(s string) []llm.ToolCall {
	if s == "" {
		return nil
	}
	var in []storedCall
	if json.Unmarshal([]byte(s), &in) != nil {
		return nil
	}
	out := make([]llm.ToolCall, len(in))
	for i, c := range in {
		out[i] = llm.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return out
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
