package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/proto"
)

// callTools streams several tool calls in one round: each is {id, name, args}.
func callTools(calls ...[3]string) []string {
	var out []string
	for i, c := range calls {
		out = append(out, call(i, c[0], c[1], c[2]))
	}
	return append(out, fin("tool_calls"), usageChunk(20, 8), "[DONE]")
}

// gate lets a test see how many tools ran at the same time.
type gate struct {
	mu      sync.Mutex
	running int
	peak    int
	order   []string
}

func (g *gate) enter(name string) {
	g.mu.Lock()
	g.running++
	g.peak = max(g.peak, g.running)
	g.order = append(g.order, name)
	g.mu.Unlock()
}

func (g *gate) leave() {
	g.mu.Lock()
	g.running--
	g.mu.Unlock()
}

func gatedTool(g *gate, name string, effect Effect, hold time.Duration) Tool {
	return Tool{
		Name: name, Description: name, Schema: json.RawMessage(`{"type":"object"}`), Effect: effect,
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			g.enter(name)
			defer g.leave()
			select {
			case <-time.After(hold):
			case <-ctx.Done():
			}
			return Result{Content: "result of " + name}, nil
		},
	}
}

func toolContents(req llmReq) []string {
	var out []string
	for _, m := range req.Messages {
		if m["role"] == "tool" {
			out = append(out, fmt.Sprint(m["content"]))
		}
	}
	return out
}

func TestReadToolsRunInParallelAndKeepOrder(t *testing.T) {
	g := &gate{}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTools(
				[3]string{"c1", "r1", `{}`}, [3]string{"c2", "r2", `{}`}, [3]string{"c3", "r3", `{}`},
				[3]string{"c4", "r4", `{}`}, [3]string{"c5", "r5", `{}`})
		}
		return 200, say("Done.")
	}, envOpts{tools: []Tool{
		gatedTool(g, "r1", EffectRead, 150*time.Millisecond), gatedTool(g, "r2", EffectRead, 10*time.Millisecond),
		gatedTool(g, "r3", EffectRead, 80*time.Millisecond), gatedTool(g, "r4", EffectRead, 10*time.Millisecond),
		gatedTool(g, "r5", EffectRead, 10*time.Millisecond),
	}})
	e.agent.Say("c", "i", "look everything up", false)
	ev := e.hub.waitTurnEnd(t, 1)

	if g.peak < 2 {
		t.Errorf("reads did not overlap: peak = %d", g.peak)
	}
	if g.peak > maxParallelReads {
		t.Errorf("peak = %d, want at most %d", g.peak, maxParallelReads)
	}
	// Results go back in the model's call order, whatever finished first.
	got := toolContents(e.llm.requests()[1])
	want := []string{"result of r1", "result of r2", "result of r3", "result of r4", "result of r5"}
	if !slices.Equal(got, want) {
		t.Errorf("tool results = %q, want %q", got, want)
	}
	if n := len(ofType[proto.Activity](ev)); n != 10 {
		t.Errorf("activity events = %d, want 10", n)
	}
}

func TestWritesStayOrderedAndSplitReadRuns(t *testing.T) {
	g := &gate{}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTools(
				[3]string{"c1", "w1", `{}`}, [3]string{"c2", "w2", `{}`},
				[3]string{"c3", "r1", `{}`}, [3]string{"c4", "r2", `{}`},
				[3]string{"c5", "w3", `{}`}, [3]string{"c6", "r3", `{}`})
		}
		return 200, say("Done.")
	}, envOpts{tools: []Tool{
		gatedTool(g, "w1", EffectLocal, 30*time.Millisecond), gatedTool(g, "w2", EffectExternal, 5*time.Millisecond),
		gatedTool(g, "w3", EffectLocal, 5*time.Millisecond),
		gatedTool(g, "r1", EffectRead, 40*time.Millisecond), gatedTool(g, "r2", EffectRead, 5*time.Millisecond),
		gatedTool(g, "r3", EffectRead, 5*time.Millisecond),
	}})
	e.agent.Say("c", "i", "do things", false)
	e.hub.waitTurnEnd(t, 1)

	// w1 must finish before w2 starts, and w3 waits for the reads before it.
	if g.order[0] != "w1" || g.order[1] != "w2" || g.order[4] != "w3" || g.order[5] != "r3" {
		t.Errorf("start order = %v", g.order)
	}
	want := []string{"result of w1", "result of w2", "result of r1", "result of r2", "result of w3", "result of r3"}
	if got := toolContents(e.llm.requests()[1]); !slices.Equal(got, want) {
		t.Errorf("tool results = %q", got)
	}
	// Only the two adjacent reads could overlap.
	if g.peak > 2 {
		t.Errorf("peak = %d, want at most 2", g.peak)
	}
}

func TestParallelReadsCountAgainstTheCallBudget(t *testing.T) {
	var ran atomic.Int32
	counter := Tool{Name: "r", Description: "r", Schema: json.RawMessage(`{"type":"object"}`), Effect: EffectRead,
		Run: func(context.Context, json.RawMessage) (Result, error) {
			ran.Add(1)
			return Result{Content: "ok"}, nil
		}}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTools([3]string{"a", "r", `{}`}, [3]string{"b", "r", `{}`}, [3]string{"c", "r", `{}`})
		}
		return 200, say("Done.")
	}, envOpts{tools: []Tool{counter}, limits: Limits{MaxCalls: 2}})
	e.agent.Say("c", "i", "go", false)
	e.hub.waitTurnEnd(t, 1)

	if ran.Load() != 2 {
		t.Errorf("ran %d calls, want 2", ran.Load())
	}
	got := toolContents(e.llm.requests()[1])
	if len(got) != 3 || !strings.HasPrefix(got[2], "error: too many tool calls") {
		t.Errorf("results = %q", got)
	}
}

func TestToolsKnowTheirTurn(t *testing.T) {
	var seen atomic.Value
	tool := Tool{Name: "who", Description: "who", Schema: json.RawMessage(`{"type":"object"}`), Effect: EffectRead,
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			seen.Store(TurnID(ctx))
			return Result{Content: "ok"}, nil
		}}
	e := newEnv(t, func(n int, _ llmReq) (int, []string) {
		if n == 0 {
			return 200, callTool("", "c", "who", `{}`)
		}
		return 200, say("ok")
	}, envOpts{tools: []Tool{tool}})
	e.agent.Say("c", "i", "who am i", false)
	ev := e.hub.waitTurnEnd(t, 1)
	if id, _ := seen.Load().(string); id == "" || id != ofType[proto.TurnEnd](ev)[0].TurnID {
		t.Errorf("tool saw turn %q", id)
	}
}
