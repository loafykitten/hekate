package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/perform"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

// roundRec is one LLM request within a turn and what came of it.
type roundRec struct {
	index   int
	model   string
	text    string // raw text the model wrote this round
	calls   []llm.ToolCall
	results map[string]string // tool call id -> result sent back to the model
	stored  map[string]string // tool call id -> what history keeps instead, when it differs
}

// complete reports whether every call has a result, so the round can be
// replayed to the model later.
func (r *roundRec) complete() bool { return len(r.results) == len(r.calls) }

// turnRun is the state of one running turn.
type turnRun struct {
	a      *Agent
	t      *turn
	log    *slog.Logger
	start  time.Time
	pipe   *pipeline
	msgs   []llm.Message
	rounds []*roundRec

	model    config.Model
	deeper   bool
	calls    int
	usage    proto.TurnUsage
	lastUsed string
	firstSeg time.Duration
	settle   func(given bool) // the opener's, told how the turn ended
}

func (a *Agent) runTurn(t *turn, prev *turn) {
	defer a.wg.Done()
	defer close(t.done)
	defer t.cancel(nil)
	if prev != nil {
		<-prev.done
	}

	ctx, cancel := context.WithTimeout(t.ctx, a.limits.TurnTimeout)
	defer cancel()

	r := &turnRun{a: a, t: t, start: time.Now(), model: a.chat, lastUsed: a.chat.Name,
		log: a.log.With("turn_id", t.id)}
	r.log.Info("turn start", "origin", t.origin, "chars", len(t.userText), "quiet", t.quiet)

	if err := r.begin(); err != nil {
		r.log.Error("cannot start turn", "error", err)
		a.hub.Broadcast(proto.NewError(proto.CodeLLMError, "Something went wrong on my side. Try again?", t.inputID))
		return
	}
	a.hub.Broadcast(proto.NewTurnStart(t.id, t.inputID, t.origin, t.userText))

	synth := a.synth
	if t.quiet {
		synth = nil
	}
	r.pipe = newPipeline(ctx, synth, a.vocabulary(), r.emitSegment, r.log)

	loopErr := r.loop(ctx)
	r.pipe.finish()
	r.end(ctx, loopErr)
}

// begin records the turn and the user's message, then builds the prompt from
// the stored history (which now ends with that message).
func (r *turnRun) begin() error {
	a, t := r.a, r.t
	ctx, cancel := a.dbCtx()
	defer cancel()
	if err := a.store.BeginTurn(ctx, store.Turn{ID: t.id, ConversationID: ConversationID,
		InputID: t.inputID, Origin: t.origin, UserText: t.userText, StartedAt: r.start}); err != nil {
		return fmt.Errorf("record turn: %w", err)
	}
	if err := a.store.AddMessages(ctx, []store.Message{{
		ConversationID: ConversationID, TurnID: t.id, Role: store.RoleUser,
		Text: t.userText, Content: t.userText, CreatedAt: r.start,
	}}); err != nil {
		return fmt.Errorf("record message: %w", err)
	}
	hist, err := a.store.RecentMessages(ctx, ConversationID, historyMessages)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}
	now := a.now()
	r.msgs = BuildMessages(PromptInput{
		Tamer: a.tamer, Gestures: a.Gestures(), Now: now, Location: a.loc, History: hist,
		Context: gatherContext(t.ctx, a.sources, now, t.userText, r.log),
	})
	return nil
}

func (r *turnRun) emitSegment(seg perform.Segment) {
	if seg.Seq == 0 {
		r.firstSeg = time.Since(r.start)
		r.log.Info("first segment", "ms", r.firstSeg.Milliseconds(), "audio", seg.AudioB64 != nil)
	}
	r.t.lastSeq.Store(int64(seg.Seq))
	r.a.hub.Broadcast(proto.NewSegment(r.t.id, seg))
}

// outOfToolsNote asks for an answer once the turn's tool budget is spent.
const outOfToolsNote = "You can't use any more tools this turn. Answer now in plain words with what you " +
	"have, and say plainly if something didn't work."

// loop runs LLM rounds until the model answers without calling tools.
func (r *turnRun) loop(ctx context.Context) error {
	a := r.a
	first, err := r.open(ctx)
	if err != nil {
		return err
	}
	for round := first; ; round++ {
		allowTools := round < a.limits.MaxRounds && r.calls < a.limits.MaxCalls
		var specs []llm.Tool
		if allowTools {
			if r.deeper {
				specs = a.tools.Specs(ThinkDeeper)
			} else {
				specs = a.tools.Specs()
			}
		}
		if !allowTools && round > 0 {
			// Without this the model, offered no tools after a run of tool
			// calls, sometimes ends the turn with nothing to say.
			r.msgs = append(r.msgs, llm.Message{Role: llm.RoleSystem, Content: outOfToolsNote})
		}
		rd := &roundRec{index: round, model: r.model.Name, results: map[string]string{}, stored: map[string]string{}}
		r.rounds = append(r.rounds, rd)
		if err := r.llmRound(ctx, rd, specs); err != nil {
			return err
		}
		if len(rd.calls) == 0 || !allowTools {
			rd.calls = nil // calls past the budget are ignored; the text stands
			return nil
		}
		r.msgs = append(r.msgs, llm.Message{Role: llm.RoleAssistant, Content: rd.text, ToolCalls: rd.calls})
		outcomes := r.runCalls(ctx, rd.calls)
		for i, call := range rd.calls {
			o := outcomes[i]
			if !o.executed {
				return ctx.Err()
			}
			if o.escalate && !r.deeper {
				r.model, r.deeper = a.deep, true
				r.log.Info("escalating to the deep model", "model", a.deep.Name)
			}
			rd.results[call.ID] = o.content
			if o.stored != "" {
				rd.stored[call.ID] = o.stored
			}
			r.msgs = append(r.msgs, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: o.content})
		}
	}
}

// open runs the opener's call, if it has one for this turn, as round 0: as if
// the model had made it, so the reply answers its result. It returns the
// round the model starts at.
func (r *turnRun) open(ctx context.Context) (int, error) {
	if r.a.opener == nil {
		return 0, nil
	}
	op, ok := r.a.opener(ctx, r.t.userText)
	if !ok {
		return 0, nil
	}
	r.settle = op.Settle
	call := llm.ToolCall{ID: "call_open", Name: op.Tool, Arguments: "{}"}
	rd := &roundRec{index: 0, model: r.model.Name, calls: []llm.ToolCall{call}, results: map[string]string{}, stored: map[string]string{}}
	r.rounds = append(r.rounds, rd)
	r.msgs = append(r.msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: rd.calls})
	o := r.runCalls(ctx, rd.calls)[0]
	if !o.executed {
		return 0, ctx.Err()
	}
	r.log.Info("turn opened with a call", "tool", op.Tool)
	rd.results[call.ID] = o.content
	if o.stored != "" {
		rd.stored[call.ID] = o.stored
	}
	r.msgs = append(r.msgs, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: o.content})
	return 1, nil
}

// llmRound makes one streaming request, feeding visible text to the pipeline.
func (r *turnRun) llmRound(ctx context.Context, rd *roundRec, specs []llm.Tool) error {
	a := r.a
	began := time.Now()
	st, err := a.llm.Stream(ctx, llm.Request{
		Model: r.model.Name, Effort: r.model.Effort, Messages: r.msgs, Tools: specs, MaxTokens: maxReplyTokens,
	})
	if err != nil {
		return err //nolint:wrapcheck // classified llm.Error or context error, inspected by end
	}
	defer func() { _ = st.Close() }()

	var split perform.Splitter
	var text strings.Builder
	for {
		ev, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err //nolint:wrapcheck // see above
		}
		switch ev.Kind {
		case llm.EventText:
			text.WriteString(ev.Text)
			for _, s := range split.Push(ev.Text) {
				r.pipe.submit(rd.index, s)
			}
		case llm.EventToolCall:
			rd.calls = append(rd.calls, ev.ToolCall)
		case llm.EventDone:
			r.recordUsage(rd, ev.Result, time.Since(began))
		}
	}
	// Text before a tool call is spoken too.
	for _, s := range split.Flush() {
		r.pipe.submit(rd.index, s)
	}
	rd.text = text.String()
	return nil
}

func (r *turnRun) recordUsage(rd *roundRec, res *llm.Result, took time.Duration) {
	r.usage.InputTokens += res.Usage.PromptTokens
	r.usage.OutputTokens += res.Usage.CompletionTokens
	if res.CostUSD != nil {
		sum := *res.CostUSD
		if r.usage.CostUSD != nil {
			sum += *r.usage.CostUSD
		}
		r.usage.CostUSD = &sum
	}
	r.lastUsed = rd.model
	ctx, cancel := r.a.dbCtx()
	defer cancel()
	err := r.a.store.AddUsage(ctx, store.Usage{
		TurnID: r.t.id, Model: rd.model, Tier: res.ServiceTier,
		InputTokens: res.Usage.PromptTokens, OutputTokens: res.Usage.CompletionTokens,
		CachedTokens: res.Usage.CachedTokens, CostUSD: res.CostUSD,
		LatencyMS: took.Milliseconds(), RequestID: res.RequestID,
	})
	if err != nil {
		r.log.Error("cannot record usage", "error", err)
	}
	r.log.Info("llm round", "round", rd.index, "model", rd.model, "tier", res.ServiceTier,
		"in", res.Usage.PromptTokens, "out", res.Usage.CompletionTokens, "cached", res.Usage.CachedTokens,
		"finish", res.FinishReason, "ms", took.Milliseconds(), "request_id", res.RequestID)
}

// toolOutcome is what one call produced.
type toolOutcome struct {
	content  string
	stored   string // what history keeps instead of content; empty: content
	executed bool   // false when the turn was cancelled before the call had a result
	escalate bool   // a successful think_deeper
}

// runCalls executes one round of calls. Consecutive read-only calls run in
// parallel (bounded); anything that changes data runs alone and in order, so a
// read after a write sees the write. Outcomes keep the model's call order.
func (r *turnRun) runCalls(ctx context.Context, calls []llm.ToolCall) []toolOutcome {
	out := make([]toolOutcome, len(calls))
	// Number the calls up front: the per-turn budget follows the model's order.
	first := r.calls + 1
	r.calls += len(calls)

	for i := 0; i < len(calls); {
		j := i
		for j < len(calls) && r.isRead(calls[j]) {
			j++
		}
		if j-i < 2 { // a lone call, or a non-read one
			j = i + 1
		}
		if j-i == 1 {
			out[i] = r.runTool(ctx, calls[i], first+i)
			if !out[i].executed {
				return out
			}
			i = j
			continue
		}
		sem := make(chan struct{}, maxParallelReads)
		var wg sync.WaitGroup
		for k := i; k < j; k++ {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				out[k] = r.runTool(ctx, calls[k], first+k)
			})
		}
		wg.Wait()
		for k := i; k < j; k++ {
			if !out[k].executed {
				return out
			}
		}
		i = j
	}
	return out
}

func (r *turnRun) isRead(call llm.ToolCall) bool {
	t, ok := r.a.tools.Get(call.Name)
	return ok && t.Effect == EffectRead
}

// runTool executes one call, the n-th of the turn. It is safe to run several
// at once: it touches only the hub and the store.
func (r *turnRun) runTool(ctx context.Context, call llm.ToolCall, n int) toolOutcome {
	a := r.a
	tool, found := a.tools.Get(call.Name)
	label := "Working on it"
	if found {
		label = tool.Label
	}
	a.hub.Broadcast(proto.NewActivity(r.t.id, call.Name, label, proto.ActivityStart))

	var (
		res    Result
		runErr error
		began  = time.Now()
	)
	args := json.RawMessage(strings.TrimSpace(call.Arguments))
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	switch {
	case !found:
		runErr = fmt.Errorf("unknown tool %q", call.Name)
	case n > a.limits.MaxCalls:
		runErr = errors.New("too many tool calls this turn; answer with what you have")
	case !json.Valid(args):
		runErr = errors.New("arguments are not valid JSON")
	default:
		tctx, cancel := context.WithTimeout(withTurnID(ctx, r.t.id), tool.Timeout)
		res, runErr = safeRun(tctx, tool, args)
		cancel()
	}
	if runErr != nil && ctx.Err() != nil {
		return toolOutcome{} // cancelled mid-call: no result to replay
	}

	state, status := proto.ActivityDone, "ok"
	content := res.Content
	if runErr != nil {
		state, status = proto.ActivityError, "error"
		content = "error: " + runErr.Error()
	}
	content = clipRunes(content, maxToolResultChars)
	stored := ""
	if runErr == nil && res.Stored != "" {
		stored = clipRunes(res.Stored, maxToolResultChars)
	}
	a.hub.Broadcast(proto.NewActivity(r.t.id, call.Name, label, state))
	if runErr == nil && res.Card != nil {
		a.hub.Broadcast(proto.NewCard(r.t.id, *res.Card))
	}
	if runErr == nil {
		for _, c := range res.Cards {
			a.hub.Broadcast(proto.NewCard(r.t.id, c))
		}
	}

	dbctx, cancel := a.dbCtx()
	defer cancel()
	effect := ""
	if found {
		effect = string(tool.Effect)
	}
	if err := a.store.AddActivity(dbctx, store.Activity{
		TurnID: r.t.id, Tool: call.Name, Effect: effect, Args: call.Arguments,
		Result: clipRunes(cmp.Or(stored, content), 200), Status: status, DurationMS: time.Since(began).Milliseconds(),
	}); err != nil {
		r.log.Error("cannot record activity", "error", err)
	}
	return toolOutcome{content: content, stored: stored, executed: true, escalate: runErr == nil && call.Name == ThinkDeeper}
}

// safeRun runs a tool and turns a panic into an error.
func safeRun(ctx context.Context, t Tool, args json.RawMessage) (res Result, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("tool panicked: %v", rec)
		}
	}()
	return t.Run(ctx, args)
}

// testHookBeforeEnd lets tests interleave an Interrupt right before end runs.
var testHookBeforeEnd func(*turn)

// end persists what was said and tells the clients how the turn went.
func (r *turnRun) end(ctx context.Context, loopErr error) {
	a, t := r.a, r.t
	if testHookBeforeEnd != nil {
		testHookBeforeEnd(t)
	}
	// endMu is held from deciding the status until fin is set, so a concurrent
	// Interrupt either cancels the turn before its status is decided (and is
	// honored here) or sees fin and trims the saved reply afterwards.
	t.endMu.Lock()
	defer t.endMu.Unlock()
	status, errCode, errMsg := proto.StatusDone, "", ""
	segs := r.pipe.segments()

	switch cause := context.Cause(ctx); {
	case loopErr == nil && cause == nil && ctx.Err() == nil:
		if len(segs) == 0 {
			status, errCode, errMsg = proto.StatusError, proto.CodeEmptyReply, "I had nothing to say. Try asking again?"
		}
	case errors.Is(cause, context.DeadlineExceeded):
		status, errCode, errMsg = proto.StatusError, proto.CodeTimeout, "That took too long, so I stopped. Want to try again?"
	case ctx.Err() != nil:
		status = proto.StatusInterrupted
	default:
		status = proto.StatusError
		errCode, errMsg = llmFailure(loopErr)
		r.log.Warn("turn failed", "error", loopErr)
	}

	// Keep only what the user heard of an interrupted reply.
	keep := t.delivered()
	if status == proto.StatusInterrupted {
		if h := t.heard.Load(); h != heardUnset {
			keep = min(int(h), keep)
		}
	}
	msgs, text := collectMessages(t.id, r.rounds, segs, keep)

	dbctx, cancel := a.dbCtx()
	defer cancel()
	if err := a.store.AddMessages(dbctx, msgs); err != nil {
		r.log.Error("cannot save reply", "error", err)
	}
	if err := a.store.EndTurn(dbctx, t.id, status, r.lastUsed, errMsg); err != nil {
		r.log.Error("cannot close turn", "error", err)
	}

	if errCode != "" {
		a.hub.Broadcast(proto.NewError(errCode, errMsg, t.inputID))
	}
	if r.settle != nil {
		r.settle(status == proto.StatusDone)
	}
	a.hub.Broadcast(proto.NewTurnEnd(t.id, status, text, r.lastUsed, r.usage))
	t.fin = &finished{rounds: r.rounds, segs: segs, kept: keep, model: r.lastUsed, usage: r.usage, at: time.Now(), settle: r.settle, plays: r.pipe.playsUntil()}
	r.log.Info("turn end", "status", status, "segments", len(segs), "kept", keep+1,
		"ms", time.Since(r.start).Milliseconds(), "first_segment_ms", r.firstSeg.Milliseconds(),
		"in", r.usage.InputTokens, "out", r.usage.OutputTokens)
}

// collectMessages builds the assistant and tool messages to store, keeping
// segments up to seq keep. A round of tool calls is stored only if all its
// results exist and nothing before it was lost, so the history always replays.
// It also returns the kept display text.
func collectMessages(turnID string, rounds []*roundRec, segs []segRecord, keep int) ([]store.Message, string) {
	var (
		out   []store.Message
		texts []string
	)
	for _, rd := range rounds {
		var raws, shown []string
		for _, s := range segs {
			if s.round == rd.index && s.seq <= keep {
				raws, shown = append(raws, s.raw), append(shown, s.display)
			}
		}
		content, text := strings.Join(raws, " "), strings.Join(shown, " ")
		if text != "" {
			texts = append(texts, text)
		}
		base := store.Message{ConversationID: ConversationID, TurnID: turnID, Role: store.RoleAssistant,
			Text: text, Content: content, Model: rd.model}
		if len(rd.calls) == 0 {
			if text != "" {
				out = append(out, base)
			}
			continue
		}
		if !rd.complete() {
			break
		}
		base.ToolCalls = encodeToolCalls(rd.calls)
		out = append(out, base)
		for _, c := range rd.calls {
			content, ok := rd.stored[c.ID]
			if !ok {
				content = rd.results[c.ID]
			}
			out = append(out, store.Message{ConversationID: ConversationID, TurnID: turnID, Role: store.RoleTool,
				Content: content, ToolCallID: c.ID})
		}
	}
	return out, strings.Join(texts, " ")
}

// llmFailure turns a provider error into a code and a message in her voice.
func llmFailure(err error) (code, message string) {
	switch llm.KindOf(err) {
	case llm.KindAuth:
		return proto.CodeLLMUnavailable, "I can't reach my thinking: the service refused my key."
	case llm.KindRateLimit:
		return proto.CodeLLMUnavailable, "I'm being asked to slow down. Give me a moment and try again."
	case llm.KindTransient:
		return proto.CodeLLMUnavailable, "I lost my connection for a moment. Try again?"
	case llm.KindContextTooLong:
		return proto.CodeLLMError, "That was too much for me to hold at once."
	case llm.KindBadRequest, llm.KindProtocol:
	}
	return proto.CodeLLMError, "Something went wrong while I was thinking. Try again?"
}
