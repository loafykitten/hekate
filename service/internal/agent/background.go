package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
)

// RecordBackgroundUsage stores what an LLM request made outside a conversation
// turn cost, under source (mail, reflection), so self-inspection can
// add it up. A nil result (the provider reported nothing) is skipped.
func RecordBackgroundUsage(ctx context.Context, st *store.Store, source string, res *llm.Result, log *slog.Logger) {
	if res == nil || st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := st.AddBackgroundUsage(ctx, source, store.Usage{
		Model: res.Model, Tier: res.ServiceTier,
		InputTokens: res.Usage.PromptTokens, OutputTokens: res.Usage.CompletionTokens,
		CachedTokens: res.Usage.CachedTokens, CostUSD: res.CostUSD,
	})
	if err != nil && log != nil {
		log.Error("cannot record background usage", "source", source, "error", err)
	}
}
