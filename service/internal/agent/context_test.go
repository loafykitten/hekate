package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSource func(ctx context.Context, now time.Time) (string, error)

func (f fakeSource) PromptContext(ctx context.Context, now time.Time) (string, error) {
	return f(ctx, now)
}

type fakeQuerySource struct {
	fakeSource
	got atomic.Value
}

func (f *fakeQuerySource) PromptContextFor(_ context.Context, _ time.Time, text string) (string, error) {
	f.got.Store(text)
	return "recalled for: " + text, nil
}

func systemPrompt(t *testing.T, e *env) string {
	t.Helper()
	reqs := e.llm.requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached the model")
	}
	return reqs[0].Messages[0]["content"].(string)
}

func TestContextSourcesFillTheNowBlock(t *testing.T) {
	query := &fakeQuerySource{}
	plain := fakeSource(func(_ context.Context, now time.Time) (string, error) {
		return "Next event at " + now.Format("15:04"), nil
	})
	e := newEnv(t, func(int, llmReq) (int, []string) { return 200, say("Hi.") }, envOpts{sources: []Source{
		{"Calendar", plain},
		{"Empty", fakeSource(func(context.Context, time.Time) (string, error) { return "  \n", nil })},
		{"Broken", fakeSource(func(context.Context, time.Time) (string, error) { return "never shown", errors.New("boom") })},
		{"What you know about {{tamer}}", query},
	}})
	e.agent.Say("c", "i", "what is up", false)
	e.hub.waitTurnEnd(t, 1)

	p := systemPrompt(t, e)
	now := strings.Index(p, "## Now")
	cal := strings.Index(p, "### Calendar\n\nNext event at 14:05")
	mem := strings.Index(p, "### What you know about Rika\n\nrecalled for: what is up")
	if now < 0 || cal < now || mem < cal {
		t.Errorf("blocks out of place (now=%d cal=%d mem=%d):\n%s", now, cal, mem, p)
	}
	for _, bad := range []string{"Empty", "Broken", "never shown"} {
		if strings.Contains(p, bad) {
			t.Errorf("prompt contains %q", bad)
		}
	}
	if query.got.Load() != "what is up" {
		t.Errorf("query source saw %v", query.got.Load())
	}
}

func TestSlowContextSourceIsLeftOut(t *testing.T) {
	slow := fakeSource(func(ctx context.Context, _ time.Time) (string, error) {
		<-ctx.Done()
		return "LATE-ANSWER", nil // ignores the deadline and answers anyway
	})
	e := newEnv(t, func(int, llmReq) (int, []string) { return 200, say("Hi.") }, envOpts{sources: []Source{
		{"Slow", slow},
		{"Fast", fakeSource(func(context.Context, time.Time) (string, error) { return "quick", nil })},
	}})
	began := time.Now()
	e.agent.Say("c", "i", "hello", false)
	e.hub.waitTurnEnd(t, 1)

	if took := time.Since(began); took > 4*time.Second {
		t.Errorf("turn took %v; the slow source should cost at most its timeout", took)
	}
	p := systemPrompt(t, e)
	if strings.Contains(p, "LATE-ANSWER") || !strings.Contains(p, "### Fast\n\nquick") {
		t.Errorf("prompt: %s", p)
	}
}

func TestPanickingContextSourceIsContained(t *testing.T) {
	e := newEnv(t, func(int, llmReq) (int, []string) { return 200, say("Hi.") }, envOpts{sources: []Source{
		{"Bad", fakeSource(func(context.Context, time.Time) (string, error) { panic("oops") })},
	}})
	e.agent.Say("c", "i", "hello", false)
	e.hub.waitTurnEnd(t, 1)
	if strings.Contains(systemPrompt(t, e), "### Bad") {
		t.Error("panicking source leaked into the prompt")
	}
}
