package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/persona"
)

// History window. It starts at a fixed message and only grows at its end, so
// the provider's prompt cache keeps matching from one turn to the next. Past
// the high limits it is cut back to the low ones in one step and grows again.
// Characters are a rough proxy for tokens that avoids shipping a tokenizer.
const (
	historyMaxMessages = 40
	historyMaxChars    = 32000
	historyLowMessages = 20
	historyLowChars    = 16000
	historyToolChars   = 1500 // tool results are long-lived noise; keep the head
)

// PromptInput is everything that goes into one turn's prompt.
type PromptInput struct {
	Tamer    string
	Gestures []string
	Now      time.Time // converted to Location for display
	Location *time.Location
	// History is the window to send (see historyWindow), oldest first, ending
	// with the current user message.
	History []store.Message
	// Context holds what the context sources contributed; it follows the clock
	// in the "Now" section, each block under its source's name.
	Context []ContextBlock
}

// BuildMessages lays the prompt out for the provider's prefix cache: first
// what never changes (persona and rules), then the history, which only grows
// at its end between window cuts, then what changes every turn (the clock and
// the context sources) as a system message just before the current input. The
// caller appends nothing for the current input: it is the last history message.
func BuildMessages(in PromptInput) []llm.Message {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: SystemPrompt(in)}}
	hist := make([]llm.Message, len(in.History))
	for i, m := range in.History {
		hist[i] = toLLM(m)
	}
	now := llm.Message{Role: llm.RoleSystem, Content: NowPrompt(in)}
	if n := len(hist); n > 0 && hist[n-1].Role == llm.RoleUser {
		msgs = append(msgs, hist[:n-1]...)
		return append(msgs, now, hist[n-1])
	}
	return append(append(msgs, hist...), now)
}

// SystemPrompt renders persona + rules: the same text every turn.
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
	return b.String()
}

// NowPrompt renders the clock and the context sources' blocks.
func NowPrompt(in PromptInput) string {
	tamer := strings.TrimSpace(in.Tamer)
	if tamer == "" {
		tamer = "Tamer"
	}
	var b strings.Builder
	b.WriteString(nowBlock(in.Now, in.Location))
	for _, c := range in.Context {
		b.WriteString("\n\n### " + strings.NewReplacer("{{tamer}}", tamer).Replace(c.Name) + "\n\n" + c.Text)
	}
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

// historyWindow picks the history to send from hist (oldest first, ending
// with the current input): from the message anchor on while that fits the high
// limits, else the newest messages within the low limits. A window always
// starts at a user message, so no tool result is left without its call. It
// returns the window and the id of its first message, the next turn's anchor.
func historyWindow(hist []store.Message, anchor int64) ([]store.Message, int64) {
	if len(hist) == 0 {
		return nil, 0
	}
	if i := slices.IndexFunc(hist, func(m store.Message) bool { return m.ID == anchor }); anchor != 0 && i >= 0 {
		if w := hist[i:]; len(w) <= historyMaxMessages && historyChars(w) <= historyMaxChars {
			return w, anchor
		}
	}
	w := hist[max(len(hist)-historyLowMessages, 0):]
	total := historyChars(w)
	// Drop the oldest messages while over budget, but never the last one.
	for len(w) > 1 && total > historyLowChars {
		total -= historyChars(w[:1])
		w = w[1:]
	}
	for len(w) > 1 && w[0].Role != store.RoleUser {
		w = w[1:]
	}
	return w, w[0].ID
}

// historyChars is the size of ms as sent, tool results clipped.
func historyChars(ms []store.Message) int {
	n := 0
	for _, m := range ms {
		c := utf8.RuneCountInString(m.Content)
		if m.Role == store.RoleTool {
			c = min(c, historyToolChars+1)
		}
		n += c + len(m.ToolCalls)
	}
	return n
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
