package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

func segTexts(ev []any) []string {
	var out []string
	for _, s := range ofType[proto.Segment](ev) {
		out = append(out, s.Text)
	}
	return out
}

func TestTextOnlyTurn(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("{waving} Morning, ", "Rika. Did you sl", "eep well? [chuck", "les] Good.")
	}, envOpts{})

	e.agent.Say("client-1", "in-1", "hey, I just woke up", false)
	ev := e.hub.waitTurnEnd(t, 1)

	start := ofType[proto.TurnStart](ev)
	if len(start) != 1 || start[0].UserText != "hey, I just woke up" || start[0].Origin != "client-1" || start[0].InputID != "in-1" {
		t.Fatalf("turn_start = %+v", start)
	}
	if got := segTexts(ev); !slices.Equal(got, []string{"Morning, Rika.", "Did you sleep well?", "Good."}) {
		t.Fatalf("segments = %q", got)
	}
	segs := ofType[proto.Segment](ev)
	if segs[0].TurnID != start[0].TurnID || segs[0].Seq != 0 || segs[2].Seq != 2 {
		t.Errorf("segments = %+v", segs)
	}
	if len(segs[0].Gestures) != 1 || segs[0].Gestures[0].Name != "waving" {
		t.Errorf("gestures = %+v", segs[0].Gestures)
	}
	if segs[0].AudioB64 != nil || segs[0].Words != nil {
		t.Errorf("no synth: audio must be null: %+v", segs[0])
	}
	if len(segs[2].Expressions) != 1 || segs[2].Expressions[0].Name != "chuckles" || segs[2].Text != "Good." {
		t.Errorf("expressions = %+v", segs[2])
	}
	end := ofType[proto.TurnEnd](ev)[0]
	if end.Status != proto.StatusDone || end.Text != "Morning, Rika. Did you sleep well? Good." || end.Model != "chat-model" {
		t.Errorf("turn_end = %+v", end)
	}
	if end.Usage.InputTokens != 10 || end.Usage.OutputTokens != 5 || end.Usage.CostUSD != nil {
		t.Errorf("usage = %+v", end.Usage)
	}

	// What the model was sent.
	reqs := e.llm.requests()
	if len(reqs) != 1 || reqs[0].Model != "chat-model" || reqs[0].Effort != "low" {
		t.Fatalf("requests = %+v", reqs)
	}
	if !slices.Equal(reqs[0].toolNames(), []string{"think_deeper"}) {
		t.Errorf("tools = %v", reqs[0].toolNames())
	}
	sys, _ := reqs[0].Messages[0]["content"].(string)
	for _, want := range []string{"Rika", "{waving}, {thankful}", "Monday, 19 October 2026", "14:05"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	if strings.Contains(sys, "{{") {
		t.Errorf("unfilled template in system prompt")
	}
	last := reqs[0].Messages[len(reqs[0].Messages)-1]
	if last["role"] != "user" || last["content"] != "hey, I just woke up" {
		t.Errorf("last message = %v", last)
	}

	// What was stored.
	msgs := e.messages()
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[1].Text != end.Text || !strings.HasPrefix(msgs[1].Content, "{waving} Morning, Rika.") || msgs[1].Model != "chat-model" {
		t.Errorf("assistant = %+v", msgs[1])
	}
	if n := e.count(`SELECT count(*) FROM usage WHERE model='chat-model' AND tier='standard' AND input_tokens=10 AND output_tokens=5 AND cached_tokens=1 AND request_id='req-0' AND cost_usd IS NULL`); n != 1 {
		t.Errorf("usage rows = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM turns WHERE status='done'`); n != 1 {
		t.Errorf("done turns = %d", n)
	}

	hist, err := e.agent.Messages(t.Context(), 0, 50)
	if err != nil || len(hist) != 2 || hist[0].Role != "user" || hist[1].Text != end.Text {
		t.Fatalf("history = %+v %v", hist, err)
	}
}

func TestHistoryReachesNextTurn(t *testing.T) {
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		return 200, say("Reply number " + string(rune('0'+n)) + " is here.")
	}, envOpts{})
	e.agent.Say("c", "i1", "first", false)
	e.hub.waitTurnEnd(t, 1)
	e.agent.Say("c", "i2", "second", false)
	e.hub.waitTurnEnd(t, 2)

	reqs := e.llm.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var roles []string
	for _, m := range reqs[1].Messages {
		roles = append(roles, m["role"].(string))
	}
	if !slices.Equal(roles, []string{"system", "user", "assistant", "user"}) {
		t.Errorf("roles = %v", roles)
	}
}

func TestSynthesizedSegmentsStayInOrder(t *testing.T) {
	// The first sentence is slowest; it must still be sent first.
	synth := &fakeSynth{delay: func(text string) time.Duration {
		if strings.HasPrefix(text, "First") {
			return 150 * time.Millisecond
		}
		return 0
	}}
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("First sentence is the slow one. Second one is fast. Third one too, really.")
	}, envOpts{synth: synth})
	e.agent.Say("c", "i", "go", false)
	ev := e.hub.waitTurnEnd(t, 1)

	segs := ofType[proto.Segment](ev)
	if len(segs) != 3 || !strings.HasPrefix(segs[0].Text, "First") || !strings.HasPrefix(segs[2].Text, "Third") {
		t.Fatalf("segments = %q", segTexts(ev))
	}
	for i, s := range segs {
		if s.Seq != i || s.AudioB64 == nil || s.AudioFormat == nil || *s.AudioFormat != "mp3" || len(s.Words) == 0 {
			t.Errorf("segment %d = %+v", i, s)
		}
	}
	if len(synth.spoken()) != 3 {
		t.Errorf("spoken = %q", synth.spoken())
	}
}

func TestSynthFailureFallsBackToText(t *testing.T) {
	synth := &fakeSynth{fail: func(text string) bool { return strings.HasPrefix(text, "Second") }}
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("First sentence is fine. Second sentence fails to speak. Third is fine again.")
	}, envOpts{synth: synth})
	e.agent.Say("c", "i", "go", false)
	segs := ofType[proto.Segment](e.hub.waitTurnEnd(t, 1))
	if len(segs) != 3 || segs[0].AudioB64 == nil || segs[1].AudioB64 != nil || segs[2].AudioB64 == nil {
		t.Fatalf("segments = %+v", segs)
	}
	if segs[1].Duration < 1.2 {
		t.Errorf("text-only duration = %v", segs[1].Duration)
	}
}

func TestQuietTurnSkipsSpeech(t *testing.T) {
	synth := &fakeSynth{}
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("{waving} Hi there, quietly. [laughs] Got it.")
	}, envOpts{synth: synth})
	e.agent.Say("c", "i", "hello", true)
	segs := ofType[proto.Segment](e.hub.waitTurnEnd(t, 1))
	if len(segs) != 2 || segs[0].AudioB64 != nil || segs[0].Words != nil || segs[1].AudioB64 != nil {
		t.Fatalf("segments = %+v", segs)
	}
	if segs[0].Duration <= 0 || len(segs[0].Gestures) != 1 || len(segs[1].Expressions) != 1 {
		t.Errorf("a quiet reply still needs timing, gestures and expressions: %+v", segs)
	}
	if got := synth.spoken(); len(got) != 0 {
		t.Errorf("spoken = %q", got)
	}
}

func lookupTool() Tool {
	return Tool{
		Name: "lookup", Description: "Look something up", Effect: EffectRead, Label: "Looking it up",
		Schema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		Run: func(_ context.Context, args json.RawMessage) (Result, error) {
			var a struct{ Q string }
			_ = json.Unmarshal(args, &a)
			return Result{Content: "found " + a.Q, Card: &Card{Kind: "note", ID: "n1", Title: "A note", Data: json.RawMessage(`{"x":1}`)}}, nil
		},
	}
}

func TestStoredReplacesContentInHistory(t *testing.T) {
	secret := Tool{
		Name: "read_secret", Description: "Read a private thing", Effect: EffectRead,
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(context.Context, json.RawMessage) (Result, error) {
			return Result{Content: "the private body text", Stored: "Read the thing (not kept)."}, nil
		},
	}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTool("", "call_1", "read_secret", `{}`)
		}
		return 200, say("Read it.")
	}, envOpts{tools: []Tool{secret}})
	e.agent.Say("c", "i", "read it", false)
	e.hub.waitTurnEnd(t, 1)

	// The model sees the content within the turn...
	m := e.llm.requests()[1].Messages
	if tool := m[len(m)-1]; tool["content"] != "the private body text" {
		t.Errorf("tool message to the model = %v", tool)
	}
	// ...but neither history nor the audit log keeps it.
	for _, msg := range e.messages() {
		if strings.Contains(msg.Content, "private body") {
			t.Errorf("stored message keeps the content: %+v", msg)
		}
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE role='tool' AND content='Read the thing (not kept).'`); n != 1 {
		t.Errorf("stored tool results = %d, want the placeholder", n)
	}
	if n := e.count(`SELECT count(*) FROM activity WHERE result LIKE '%private body%'`); n != 0 {
		t.Errorf("activity keeps the content")
	}
}

func TestToolRoundTrip(t *testing.T) {
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTool("Let me look that up for you.", "call_1", "lookup", `{"q":"tea"}`)
		}
		return 200, say("You have tea in the cupboard.")
	}, envOpts{tools: []Tool{lookupTool()}})
	e.agent.Say("c", "i", "do we have tea?", false)
	ev := e.hub.waitTurnEnd(t, 1)

	// Text before the call is spoken, then the answer.
	if got := segTexts(ev); !slices.Equal(got, []string{"Let me look that up for you.", "You have tea in the cupboard."}) {
		t.Fatalf("segments = %q", got)
	}
	acts := ofType[proto.Activity](ev)
	if len(acts) != 2 || acts[0].State != "start" || acts[1].State != "done" || acts[0].Label != "Looking it up" || acts[0].Tool != "lookup" {
		t.Fatalf("activity = %+v", acts)
	}
	cards := ofType[proto.CardMsg](ev)
	if len(cards) != 1 || cards[0].Card.Kind != "note" || string(cards[0].Card.Data) != `{"x":1}` {
		t.Errorf("cards = %+v", cards)
	}

	reqs := e.llm.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	m := reqs[1].Messages
	asst, tool := m[len(m)-2], m[len(m)-1]
	if asst["role"] != "assistant" || asst["tool_calls"] == nil || tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "found tea" {
		t.Errorf("round-trip messages = %v / %v", asst, tool)
	}
	if !slices.Contains(reqs[1].toolNames(), "lookup") || reqs[1].Model != "chat-model" {
		t.Errorf("second request = %+v", reqs[1])
	}

	msgs := e.messages()
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	if !slices.Equal(roles, []string{"user", "assistant", "tool", "assistant"}) {
		t.Fatalf("stored roles = %v", roles)
	}
	if msgs[1].ToolCalls == "" || msgs[1].Text != "Let me look that up for you." || msgs[2].ToolCallID != "call_1" || msgs[3].Text != "You have tea in the cupboard." {
		t.Errorf("stored = %+v", msgs)
	}
	if n := e.count(`SELECT count(*) FROM activity WHERE tool='lookup' AND effect='read' AND status='ok' AND args LIKE '%tea%' AND result='found tea'`); n != 1 {
		t.Errorf("activity rows = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM usage`); n != 2 {
		t.Errorf("usage rows = %d", n)
	}
	// The visible history hides tool traffic.
	hist, _ := e.agent.Messages(t.Context(), 0, 50)
	if len(hist) != 3 {
		t.Errorf("history = %+v", hist)
	}

	// The next turn replays the tool exchange to the model.
	e.agent.Say("c", "i2", "thanks", false)
	e.hub.waitTurnEnd(t, 2)
	reqs = e.llm.requests()
	var roles2 []string
	for _, m := range reqs[len(reqs)-1].Messages {
		roles2 = append(roles2, m["role"].(string))
	}
	if !slices.Equal(roles2, []string{"system", "user", "assistant", "tool", "assistant", "user"}) {
		t.Errorf("replayed roles = %v", roles2)
	}
}

func idx(ev []any, pred func(any) bool) int {
	for i, e := range ev {
		if pred(e) {
			return i
		}
	}
	return -1
}

func TestToolFailureIsReportedToTheModel(t *testing.T) {
	boom := Tool{Name: "boom", Description: "d", Schema: json.RawMessage(`{"type":"object"}`), Effect: EffectLocal,
		Run: func(context.Context, json.RawMessage) (Result, error) { panic("kaboom") }}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		switch n {
		case 0:
			return 200, callTool("", "c1", "boom", `{}`)
		case 1:
			return 200, callTool("", "c2", "nope", `{}`)
		}
		return 200, say("That did not work, sorry about it.")
	}, envOpts{tools: []Tool{boom}})
	e.agent.Say("c", "i", "do it", false)
	ev := e.hub.waitTurnEnd(t, 1)
	acts := ofType[proto.Activity](ev)
	states := []string{}
	for _, a := range acts {
		states = append(states, a.Tool+":"+a.State)
	}
	if !slices.Equal(states, []string{"boom:start", "boom:error", "nope:start", "nope:error"}) {
		t.Fatalf("activity = %v", states)
	}
	reqs := e.llm.requests()
	m := reqs[1].Messages
	if c, _ := m[len(m)-1]["content"].(string); !strings.Contains(c, "error: tool panicked") {
		t.Errorf("tool result = %q", c)
	}
	m = reqs[2].Messages
	if c, _ := m[len(m)-1]["content"].(string); !strings.Contains(c, "unknown tool") {
		t.Errorf("tool result = %q", c)
	}
	if n := e.count(`SELECT count(*) FROM activity WHERE status='error'`); n != 2 {
		t.Errorf("error activity rows = %d", n)
	}
}

func TestThinkDeeperSwitchesModel(t *testing.T) {
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTool("Hmm, that deserves some thought.", "td1", "think_deeper", `{"reason":"big decision"}`)
		}
		return 200, say("Here is what I think about that.")
	}, envOpts{tools: []Tool{lookupTool()}})
	e.agent.Say("c", "i", "should I take the job?", false)
	ev := e.hub.waitTurnEnd(t, 1)

	reqs := e.llm.requests()
	if len(reqs) != 2 || reqs[0].Model != "chat-model" || reqs[1].Model != "deep-model" || reqs[1].Effort != "high" {
		t.Fatalf("requests = %+v", reqs)
	}
	if slices.Contains(reqs[1].toolNames(), "think_deeper") || !slices.Contains(reqs[1].toolNames(), "lookup") {
		t.Errorf("deep tools = %v", reqs[1].toolNames())
	}
	end := ofType[proto.TurnEnd](ev)[0]
	if end.Model != "deep-model" || end.Status != "done" {
		t.Errorf("turn_end = %+v", end)
	}
	if n := e.count(`SELECT count(*) FROM usage WHERE model='chat-model'`); n != 1 {
		t.Errorf("chat usage rows = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM usage WHERE model='deep-model'`); n != 1 {
		t.Errorf("deep usage rows = %d", n)
	}
	// The tool answer told the model to carry on.
	m := reqs[1].Messages
	if c, _ := m[len(m)-1]["content"].(string); !strings.Contains(c, "Carry on") {
		t.Errorf("think_deeper result = %q", c)
	}
}

func TestInterruptKeepsHeardSegments(t *testing.T) {
	tests := []struct {
		name     string
		heard    int
		wantText string
	}{
		{"heard first", 0, "First sentence is right here."},
		{"heard two", 1, "First sentence is right here. Second sentence is also here."},
		{"heard none", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, func(int, llmReq) (int, []string) {
				return 200, sayThenHang("First sentence is right here. Second sentence is also here. Third sentence is cut")
			}, envOpts{})
			e.agent.Say("c", "i", "tell me a story", false)
			e.hub.waitFor(t, "two segments", func(ev []any) bool { return len(ofType[proto.Segment](ev)) >= 2 })
			e.agent.Interrupt("", tt.heard)
			ev := e.hub.waitTurnEnd(t, 1)

			end := ofType[proto.TurnEnd](ev)[0]
			if end.Status != proto.StatusInterrupted || end.Text != tt.wantText {
				t.Errorf("turn_end = %+v", end)
			}
			msgs := e.messages()
			if tt.wantText == "" {
				if len(msgs) != 1 || msgs[0].Role != "user" {
					t.Errorf("stored = %+v", msgs)
				}
			} else if len(msgs) != 2 || msgs[1].Text != tt.wantText {
				t.Errorf("stored = %+v", msgs)
			}
			if n := e.count(`SELECT count(*) FROM turns WHERE status='interrupted'`); n != 1 {
				t.Errorf("interrupted turns = %d", n)
			}
			if got := len(ofType[proto.Error](ev)); got != 0 {
				t.Errorf("an interrupt is not an error: %v", ofType[proto.Error](ev))
			}
		})
	}
}

func TestInterruptCancelsSynthesis(t *testing.T) {
	synth := &fakeSynth{delay: func(string) time.Duration { return time.Minute }}
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, sayThenHang("A sentence that never gets spoken. And another one here.")
	}, envOpts{synth: synth})
	e.agent.Say("c", "i", "go", false)
	e.hub.waitFor(t, "turn start", func(ev []any) bool { return len(ofType[proto.TurnStart](ev)) == 1 })
	for len(synth.spoken()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	e.agent.Interrupt("", -1)
	ev := e.hub.waitTurnEnd(t, 1)
	if len(ofType[proto.Segment](ev)) != 0 || ofType[proto.TurnEnd](ev)[0].Status != "interrupted" {
		t.Errorf("events = %s", describe(ev))
	}
}

func TestNewInputInterruptsRunningTurn(t *testing.T) {
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, sayThenHang("First reply starts here. And keeps going on")
		}
		return 200, say("Second reply is complete.")
	}, envOpts{})
	e.agent.Say("c", "i1", "one", false)
	e.hub.waitFor(t, "first segment", func(ev []any) bool { return len(ofType[proto.Segment](ev)) >= 1 })
	e.agent.Say("c", "i2", "two", false)
	ev := e.hub.waitTurnEnd(t, 2)

	ends := ofType[proto.TurnEnd](ev)
	if ends[0].Status != "interrupted" || ends[1].Status != "done" || ends[0].TurnID == ends[1].TurnID {
		t.Fatalf("ends = %+v", ends)
	}
	// The interrupted turn closes before the next one opens.
	endIdx := idx(ev, func(x any) bool { te, ok := x.(proto.TurnEnd); return ok && te.Status == "interrupted" })
	startIdx := idx(ev, func(x any) bool { ts, ok := x.(proto.TurnStart); return ok && ts.UserText == "two" })
	if endIdx > startIdx {
		t.Errorf("turn_end (%d) must precede the next turn_start (%d)", endIdx, startIdx)
	}
	// Interrupting with the last delivered segment keeps what was sent.
	msgs := e.messages()
	var texts []string
	for _, m := range msgs {
		texts = append(texts, m.Role+":"+m.Text)
	}
	want := []string{"user:one", "assistant:First reply starts here.", "user:two", "assistant:Second reply is complete."}
	if !slices.Equal(texts, want) {
		t.Errorf("stored = %q", texts)
	}
}

func TestToolBudgetEndsInAnAnswer(t *testing.T) {
	e := newEnv(t, func(_ int, req llmReq) (int, []string) {
		if len(req.Tools) > 0 {
			return 200, callTool("", "c", "lookup", `{"q":"x"}`)
		}
		return 200, say("I could not settle this, sorry about that.")
	}, envOpts{tools: []Tool{lookupTool()}, limits: Limits{MaxRounds: 3, MaxCalls: 12}})
	e.agent.Say("c", "i", "loop", false)
	ev := e.hub.waitTurnEnd(t, 1)
	reqs := e.llm.requests()
	if len(reqs) != 4 || len(reqs[3].Tools) != 0 {
		t.Fatalf("requests = %d, last tools = %v", len(reqs), reqs[len(reqs)-1].Tools)
	}
	if last := reqs[3].Messages[len(reqs[3].Messages)-1]; last["role"] != "system" || last["content"] != outOfToolsNote {
		t.Errorf("last message of the tool-less round = %v", last)
	}
	if end := ofType[proto.TurnEnd](ev)[0]; end.Status != "done" || end.Text != "I could not settle this, sorry about that." {
		t.Errorf("turn_end = %+v", end)
	}
}

func TestToolCallBudget(t *testing.T) {
	multi := func() []string {
		return []string{call(0, "a", "lookup", `{"q":"1"}`), call(1, "b", "lookup", `{"q":"2"}`), call(2, "c", "lookup", `{"q":"3"}`),
			fin("tool_calls"), usageChunk(1, 1), "[DONE]"}
	}
	e := newEnv(t, func(_ int, req llmReq) (int, []string) {
		if len(req.Tools) > 0 {
			return 200, multi()
		}
		return 200, say("That is all I can look up.")
	}, envOpts{tools: []Tool{lookupTool()}, limits: Limits{MaxRounds: 6, MaxCalls: 4}})
	e.agent.Say("c", "i", "lots", false)
	ev := e.hub.waitTurnEnd(t, 1)
	// 4 calls run (3 + 1); the other two of round two are refused. All six are audited.
	if n := e.count(`SELECT count(*) FROM activity`); n != 6 {
		t.Errorf("activity rows = %d, want 6", n)
	}
	if n := e.count(`SELECT count(*) FROM activity WHERE status='error' AND result LIKE '%too many tool calls%'`); n != 2 {
		t.Errorf("refused calls = %d, want 2", n)
	}
	reqs := e.llm.requests()
	if len(reqs[len(reqs)-1].Tools) != 0 {
		t.Errorf("last request still offers tools")
	}
	if ofType[proto.TurnEnd](ev)[0].Status != "done" {
		t.Errorf("events: %s", describe(ev))
	}
}

func TestTurnDeadline(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) { return 200, sayThenHang("Starting to say something here. And then") },
		envOpts{limits: Limits{TurnTimeout: 300 * time.Millisecond}})
	e.agent.Say("c", "i", "go", false)
	ev := e.hub.waitTurnEnd(t, 1)
	end := ofType[proto.TurnEnd](ev)[0]
	errs := ofType[proto.Error](ev)
	if end.Status != "error" || len(errs) != 1 || errs[0].Code != proto.CodeTimeout || errs[0].InputID != "i" {
		t.Fatalf("events: %s", describe(ev))
	}
	// What was said before the deadline stays in history.
	if end.Text != "Starting to say something here." {
		t.Errorf("kept text = %q", end.Text)
	}
}

func TestSlowToolHitsItsTimeout(t *testing.T) {
	slow := Tool{Name: "slow", Description: "d", Schema: json.RawMessage(`{"type":"object"}`), Effect: EffectRead, Timeout: 50 * time.Millisecond,
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		}}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTool("", "c1", "slow", `{}`)
		}
		return 200, say("That took too long, so I gave up on it.")
	}, envOpts{tools: []Tool{slow}})
	e.agent.Say("c", "i", "go", false)
	ev := e.hub.waitTurnEnd(t, 1)
	if ofType[proto.TurnEnd](ev)[0].Status != "done" {
		t.Fatalf("events: %s", describe(ev))
	}
	m := e.llm.requests()[1].Messages
	if c, _ := m[len(m)-1]["content"].(string); !strings.Contains(c, "deadline exceeded") {
		t.Errorf("tool result = %q", c)
	}
}

func TestNoAPIKey(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) { t.Error("must not call the LLM"); return 500, nil }, envOpts{noKey: true})
	e.agent.Say("c", "in-9", "hello", false)
	ev := e.hub.snapshot()
	errs := ofType[proto.Error](ev)
	if len(ev) != 1 || len(errs) != 1 || errs[0].Code != "llm_unavailable" || errs[0].InputID != "in-9" || errs[0].Message == "" {
		t.Fatalf("events: %s", describe(ev))
	}
	if len(e.messages()) != 0 {
		t.Error("nothing should be stored")
	}
}

func TestLLMFailureSurfacesAsError(t *testing.T) {
	tests := []struct {
		status int
		code   string
	}{
		{401, proto.CodeLLMUnavailable},
		{429, proto.CodeLLMUnavailable},
		{503, proto.CodeLLMUnavailable},
		{400, proto.CodeLLMError},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			e := newEnv(t, func(int, llmReq) (int, []string) { return tt.status, nil }, envOpts{})
			e.agent.Say("c", "i", "hello", false)
			ev := e.hub.waitTurnEnd(t, 1)
			errs := ofType[proto.Error](ev)
			end := ofType[proto.TurnEnd](ev)[0]
			if end.Status != "error" || len(errs) != 1 || errs[0].Code != tt.code || errs[0].Message == "" {
				t.Fatalf("events: %s", describe(ev))
			}
			if n := e.count(`SELECT count(*) FROM turns WHERE status='error'`); n != 1 {
				t.Errorf("error turns = %d", n)
			}
		})
	}
}

func TestEmptyReplyIsAnError(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) { return 200, say() }, envOpts{})
	e.agent.Say("c", "i", "hello", false)
	ev := e.hub.waitTurnEnd(t, 1)
	errs := ofType[proto.Error](ev)
	if len(errs) != 1 || errs[0].Code != proto.CodeEmptyReply {
		t.Fatalf("events: %s", describe(ev))
	}
}

func TestUnknownTagsAreNotSpoken(t *testing.T) {
	synth := &fakeSynth{}
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("{dance} Hello there, [shouts] my friend. [softly] Rest now.")
	}, envOpts{synth: synth})
	e.agent.Say("c", "i", "go", false)
	e.hub.waitTurnEnd(t, 1)
	for _, s := range synth.spoken() {
		if strings.Contains(s, "dance") || strings.Contains(s, "shouts") || strings.Contains(s, "{") {
			t.Errorf("spoken text has a bad tag: %q", s)
		}
	}
	// Sentences are synthesized in parallel, so their order here varies.
	if sp := synth.spoken(); len(sp) != 2 || !slices.Contains(sp, "[softly] Rest now.") {
		t.Errorf("spoken = %q", sp)
	}
}

func TestStartAbortsStaleTurns(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) { return 200, say("Hello there my friend.") }, envOpts{})
	if err := e.db.BeginTurn(t.Context(), store.Turn{ID: "stale", ConversationID: ConversationID, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.agent.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM turns WHERE id='stale' AND status='error'`); n != 1 {
		t.Errorf("stale turn not aborted")
	}
}

func TestShutdownInterruptsTurn(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, sayThenHang("A sentence that is done here. More to come")
	}, envOpts{})
	e.agent.Say("c", "i", "go", false)
	e.hub.waitFor(t, "segment", func(ev []any) bool { return len(ofType[proto.Segment](ev)) >= 1 })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := e.agent.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	ev := e.hub.snapshot()
	if end := ofType[proto.TurnEnd](ev); len(end) != 1 || end[0].Status != "interrupted" || end[0].Text != "A sentence that is done here." {
		t.Errorf("events: %s", describe(ev))
	}
	if err := e.agent.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second shutdown: %v", err)
	}
}

// Clients often interrupt after generation has finished, while queued audio is
// still playing. The saved reply must still be cut to what was heard.
func TestInterruptAfterTurnEndsTrimsSavedReply(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("First sentence is right here. Second sentence is also here. Third sentence ends it.")
	}, envOpts{})
	e.agent.Say("c", "i", "tell me a story", false)
	e.hub.waitTurnEnd(t, 1)

	e.agent.Interrupt("", 0)
	ev := e.hub.waitTurnEnd(t, 2)
	ends := ofType[proto.TurnEnd](ev)
	if ends[0].Status != proto.StatusDone || ends[1].Status != proto.StatusInterrupted ||
		ends[1].Text != "First sentence is right here." || ends[1].TurnID != ends[0].TurnID {
		t.Fatalf("ends = %+v", ends)
	}
	msgs := e.messages()
	if len(msgs) != 2 || msgs[1].Text != "First sentence is right here." {
		t.Errorf("stored = %+v", msgs)
	}
	if n := e.count(`SELECT count(*) FROM turns WHERE status='interrupted'`); n != 1 {
		t.Errorf("interrupted turns = %d", n)
	}

	// Hearing more than was kept, or interrupting again, changes nothing.
	e.agent.Interrupt("", 2)
	e.agent.Interrupt("", 0)
	if got := len(ofType[proto.TurnEnd](e.hub.snapshot())); got != 2 {
		t.Errorf("turn_end events = %d, want 2", got)
	}
}

// An Interrupt that lands after generation finished but before end decides the
// status must still cut the saved reply to what was heard.
func TestInterruptJustBeforeEndIsHonored(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("First sentence is right here. Second sentence is also here. Third sentence ends it.")
	}, envOpts{})
	testHookBeforeEnd = func(*turn) { e.agent.Interrupt("", 0) }
	t.Cleanup(func() { testHookBeforeEnd = nil })

	e.agent.Say("c", "i", "tell me a story", false)
	ev := e.hub.waitTurnEnd(t, 1)
	end := ofType[proto.TurnEnd](ev)[0]
	if end.Status != proto.StatusInterrupted || end.Text != "First sentence is right here." {
		t.Fatalf("turn_end = %+v", end)
	}
	if msgs := e.messages(); len(msgs) != 2 || msgs[1].Text != "First sentence is right here." {
		t.Errorf("stored = %+v", msgs)
	}
}

// A late interrupt that describes an older turn must not cut the newer one.
func TestInterruptForAnOlderTurnIsIgnored(t *testing.T) {
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, say("The first reply is short.")
		}
		return 200, sayThenHang("The second reply has started. And it goes on")
	}, envOpts{})
	e.agent.Say("phone", "i1", "one", false)
	first := ofType[proto.TurnEnd](e.hub.waitTurnEnd(t, 1))[0].TurnID
	e.agent.Say("desktop", "i2", "two", false)
	e.hub.waitFor(t, "second reply", func(ev []any) bool { return len(ofType[proto.Segment](ev)) >= 2 })

	e.agent.Interrupt(first, -1) // the phone's interrupt, arriving late
	time.Sleep(50 * time.Millisecond)
	if ends := ofType[proto.TurnEnd](e.hub.snapshot()); len(ends) != 1 {
		t.Fatalf("the newer turn was cut: %+v", ends)
	}
	e.agent.Interrupt("", 0)
	if end := ofType[proto.TurnEnd](e.hub.waitTurnEnd(t, 2))[1]; end.Status != proto.StatusInterrupted || end.TurnID == first {
		t.Errorf("end = %+v", end)
	}
}

// Someone starting push-to-talk stops her: a running turn keeps what was
// delivered; a finished one still playing somewhere is announced once.
func TestHush(t *testing.T) {
	t.Run("running turn", func(t *testing.T) {
		e := newEnv(t, func(int, llmReq) (int, []string) {
			return 200, sayThenHang("First sentence is right here. Second sentence is also here. And then")
		}, envOpts{})
		e.agent.Say("c", "i", "go", false)
		e.hub.waitFor(t, "two segments", func(ev []any) bool { return len(ofType[proto.Segment](ev)) >= 2 })
		e.agent.Hush()
		end := ofType[proto.TurnEnd](e.hub.waitTurnEnd(t, 1))[0]
		if end.Status != proto.StatusInterrupted || end.Text != "First sentence is right here. Second sentence is also here." {
			t.Errorf("end = %+v", end)
		}
	})
	t.Run("finished turn", func(t *testing.T) {
		e := newEnv(t, func(int, llmReq) (int, []string) {
			return 200, say("All of it was generated. Some may still be playing.")
		}, envOpts{})
		e.agent.Say("c", "i", "go", false)
		e.hub.waitTurnEnd(t, 1)
		e.agent.Hush()
		e.agent.Hush()
		ends := ofType[proto.TurnEnd](e.hub.waitTurnEnd(t, 2))
		if len(ends) != 2 || ends[1].Status != proto.StatusInterrupted || ends[1].TurnID != ends[0].TurnID {
			t.Fatalf("ends = %+v", ends)
		}
		time.Sleep(50 * time.Millisecond)
		if n := len(ofType[proto.TurnEnd](e.hub.snapshot())); n != 2 {
			t.Errorf("turn_end events = %d, want 2 (announced once)", n)
		}
		if msgs := e.messages(); len(msgs) != 2 || msgs[1].Text != "All of it was generated. Some may still be playing." {
			t.Errorf("stored = %+v (nothing is trimmed without a heard position)", msgs)
		}
	})
}

// A new turn starting while a late interrupt rewrites the finished reply must
// wait, so its user message is stored after the trimmed reply.
func TestTrimFinishesBeforeTheNextTurnStarts(t *testing.T) {
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, say("First sentence is right here. Second sentence is also here.")
		}
		return 200, say("The next reply.")
	}, envOpts{})
	e.agent.Say("c", "i1", "one", false)
	e.hub.waitTurnEnd(t, 1)

	testHookDuringTrim = func() {
		go e.agent.Say("c", "i2", "two", false) // must block until the trim is done
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(func() { testHookDuringTrim = nil })
	e.agent.Interrupt("", 0)
	e.hub.waitTurnEnd(t, 3)

	var got []string
	for _, m := range e.messages() {
		got = append(got, m.Role+":"+m.Text)
	}
	want := []string{"user:one", "assistant:First sentence is right here.", "user:two", "assistant:The next reply."}
	if !slices.Equal(got, want) {
		t.Errorf("history = %q\nwant      %q", got, want)
	}
}

func TestAnOpenerMakesTheFirstCall(t *testing.T) {
	brief := Tool{
		Name: "brief", Description: "The day", Effect: EffectRead, Label: "Looking over your day",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(context.Context, json.RawMessage) (Result, error) {
			return Result{Content: "Rain, and the dentist at three.", Cards: []Card{
				{Kind: "weather", ID: "w", Title: "Weather", Data: json.RawMessage(`{}`)},
				{Kind: "events", ID: "e", Title: "Coming up", Data: json.RawMessage(`{}`)},
			}}, nil
		},
	}
	opener := func(_ context.Context, text string) (string, bool) {
		return "brief", strings.Contains(text, "good morning")
	}
	e := newEnv(t, func(int, llmReq) (int, []string) {
		return 200, say("Morning, Rika. Take an umbrella to the dentist.")
	}, envOpts{tools: []Tool{brief}, opener: opener})
	e.agent.Say("c", "i", "good morning!", false)
	ev := e.hub.waitTurnEnd(t, 1)

	// The model is asked once, with the opener's call and its result already in.
	reqs := e.llm.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	m := reqs[0].Messages
	asst, tool := m[len(m)-2], m[len(m)-1]
	if asst["role"] != "assistant" || asst["tool_calls"] == nil || tool["role"] != "tool" || tool["content"] != "Rain, and the dentist at three." {
		t.Errorf("opened messages = %v / %v", asst, tool)
	}
	if cards := ofType[proto.CardMsg](ev); len(cards) != 2 || cards[0].Card.Kind != "weather" || cards[1].Card.Kind != "events" {
		t.Errorf("cards = %+v", cards)
	}
	if acts := ofType[proto.Activity](ev); len(acts) != 2 || acts[0].Label != "Looking over your day" {
		t.Errorf("activity = %+v", acts)
	}
	// History replays: user, the call, its result, the reply.
	var roles []string
	for _, msg := range e.messages() {
		roles = append(roles, msg.Role)
	}
	if !slices.Equal(roles, []string{"user", "assistant", "tool", "assistant"}) {
		t.Errorf("history roles = %v", roles)
	}

	// Anything else is left to the model.
	e.agent.Say("c", "j", "thanks", false)
	e.hub.waitTurnEnd(t, 2)
	if m := e.llm.requests()[1].Messages; m[len(m)-1]["role"] != "user" {
		t.Errorf("an unopened turn starts with %v", m[len(m)-1])
	}
}
