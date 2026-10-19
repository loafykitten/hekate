package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// contextTimeout bounds each context source, so a slow calendar server delays
// a turn by at most this much (sources run in parallel).
const contextTimeout = 1500 * time.Millisecond

// ContextSource contributes a block of text to the prompt's "Now" section: the
// next events, the weather, what she knows about the owner. It is satisfied
// structurally, so feature packages need not import agent. An empty string
// means "nothing to add".
type ContextSource interface {
	PromptContext(ctx context.Context, now time.Time) (string, error)
}

// QuerySource is a ContextSource whose text depends on what the owner just
// said, such as recalled memories. The agent prefers this method when a source
// has it; PromptContext remains the fallback with no message.
type QuerySource interface {
	ContextSource
	PromptContextFor(ctx context.Context, now time.Time, userText string) (string, error)
}

// Source is a named ContextSource. The name becomes the block's heading.
type Source struct {
	Name   string
	Source ContextSource
}

// ContextBlock is one source's contribution, ready for the prompt.
type ContextBlock struct {
	Name string
	Text string
}

// gatherContext asks every source in parallel, each under its own timeout.
// Failures and empty results are left out; the order of sources is kept.
func gatherContext(ctx context.Context, sources []Source, now time.Time, userText string, log *slog.Logger) []ContextBlock {
	texts := make([]string, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Go(func() {
			sctx, cancel := context.WithTimeout(ctx, contextTimeout)
			defer cancel()
			text, err := askSource(sctx, src.Source, now, userText)
			if err != nil {
				log.Warn("context source failed; leaving it out", "source", src.Name, "error", err)
				return
			}
			texts[i] = strings.TrimSpace(text)
		})
	}
	wg.Wait()

	var out []ContextBlock
	for i, src := range sources {
		if texts[i] != "" {
			out = append(out, ContextBlock{Name: src.Name, Text: texts[i]})
		}
	}
	return out
}

// askSource calls one source, turning a panic or a missed deadline into an error.
func askSource(ctx context.Context, src ContextSource, now time.Time, userText string) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	if qs, ok := src.(QuerySource); ok {
		text, err = qs.PromptContextFor(ctx, now, userText)
	} else {
		text, err = src.PromptContext(ctx, now)
	}
	if err == nil && ctx.Err() != nil {
		// A source that ignores its context may still return late; honour the bound.
		err = ctx.Err()
	}
	return text, err
}

type turnIDKey struct{}

// TurnID returns the id of the turn a tool is running in, or "".
func TurnID(ctx context.Context) string {
	id, _ := ctx.Value(turnIDKey{}).(string)
	return id
}

func withTurnID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, turnIDKey{}, id)
}
