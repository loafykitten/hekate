// Package agent runs Hekate's conversation: it builds the prompt, streams the
// model's reply through the sentence splitter and voice into segments, runs
// tools, and keeps the history.
package agent

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/perform"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/internal/voice"
)

// ConversationID is the main conversation: the voice one, which memory, the
// reflection and the routines read. Threads are conversations of their own.
const ConversationID = "default"

// Turn bounds from PLAN.md.
const (
	defaultMaxRounds       = 6
	defaultMaxCalls        = 12
	maxParallelReads       = 4
	defaultTurnTimeout     = 90 * time.Second
	defaultReplyTokens     = 4096
	defaultToolResultChars = 8000
	dbTimeout              = 5 * time.Second
)

var errInterrupted = errors.New("turn interrupted")

// Limits bound one turn. Zero values mean the defaults.
type Limits struct {
	MaxRounds   int // LLM rounds that may call tools
	MaxCalls    int // tool calls in total
	TurnTimeout time.Duration
	// ToolResultChars cuts each tool result sent back to the model.
	ToolResultChars int
	// ReplyTokens caps what the model writes per round, its reasoning included.
	ReplyTokens int
	// Window is the history sent with each turn.
	Window Window
}

// Options are the agent's dependencies.
type Options struct {
	// ConversationID is the conversation this agent holds; empty is the main one.
	ConversationID string
	// System, if set, builds the system prompt instead of the voice persona,
	// from the owner's name.
	System func(tamer string) string
	// Written replies are read, not performed: the model's text goes out and
	// is kept exactly as written (markdown, code, line breaks), with no tags
	// parsed and no speech.
	Written   bool
	LLM       *llm.Client
	Store     *store.Store
	Hub       Hub
	Synth     voice.Synthesizer // nil: text-only replies
	Tools     *Registry         // nil: only think_deeper
	Context   []Source          // prompt context, in the order given
	Logger    *slog.Logger
	Tamer     string
	Location  *time.Location
	Chat      config.Model
	Deep      config.Model
	AssetsDir string
	Limits    Limits
	Now       func() time.Time
	// Opener, if set, may start a turn with a tool call of its own before the
	// model is asked, such as the morning briefing on the day's first "good
	// morning". False leaves the turn alone.
	Opener func(ctx context.Context, userText string) (Opening, bool)
	// CheckIn, if set, opens the evening check-in when the owner taps it: day
	// is the day the notice asked about ("" for today's). False means there's
	// none to start (already talked over, or the notice is from another day).
	CheckIn func(ctx context.Context, day string) (Opening, bool)
}

// Opening is the call a turn starts with. Settle, if set, is told at the end
// of the turn whether the reply was given in full (not cut short or failed),
// and told again (false) if its playback is stopped partway afterwards: an
// interrupt, or new input on any device while it could still be playing.
type Opening struct {
	Tool   string
	Settle func(given bool)
}

// Agent owns a conversation and runs one turn at a time.
type Agent struct {
	conv     string
	system   func(tamer string) string
	written  bool
	llm      *llm.Client
	store    *store.Store
	hub      Hub
	synth    voice.Synthesizer
	tools    *Registry
	sources  []Source
	log      *slog.Logger
	tamer    string
	loc      *time.Location
	chat     atomic.Pointer[config.Model] // SetModel changes it between turns
	deep     config.Model
	gestures *gestureSource
	limits   Limits
	now      func() time.Time
	opener   func(ctx context.Context, userText string) (Opening, bool)
	checkIn  func(ctx context.Context, day string) (Opening, bool)

	baseCtx context.Context //nolint:containedctx // service lifetime, set by Start
	wg      sync.WaitGroup

	// anchor is the id of the history window's first message (historyWindow).
	// Turns run one at a time; it is atomic for tests that peek.
	anchor atomic.Int64

	mu  sync.Mutex
	cur *turn
}

// turn is the handle on a running or queued turn.
type turn struct {
	id       string
	inputID  string
	origin   string
	userText string
	quiet    bool     // the device that asked has her voice off: no speech for this turn
	opening  *Opening // set: the turn opens with this call instead of asking the opener
	note     string   // what the model reads after userText, or in its place when the owner said nothing

	ctx    context.Context //nolint:containedctx // the turn's lifetime
	cancel context.CancelCauseFunc
	done   chan struct{}

	lastSeq atomic.Int64 // newest segment delivered; -1 before the first
	heard   atomic.Int64 // set by interrupt; heardUnset until then

	// endMu orders an interrupt against the end of the turn: either the turn
	// sees the heard seq when it saves, or the interrupt sees fin and trims
	// the saved reply itself. Clients often interrupt after generation has
	// finished, while queued audio is still playing.
	endMu sync.Mutex
	fin   *finished
}

// finished is what a completed turn saved, kept so a late interrupt can trim it.
type finished struct {
	rounds []*roundRec
	segs   []segRecord
	kept   int              // last seq stored
	hushed bool             // clients were already told it was interrupted
	settle func(given bool) // the opener's, if the turn had one
	plays  time.Time        // estimated end of its playback
	model  string
	usage  proto.TurnUsage
	at     time.Time
}

// stopped tells the opener that the reply was stopped partway after it was
// generated. Unless the client said so (sure), that's assumed only while it
// could still be playing.
func (f *finished) stopped(sure bool) {
	if f.settle == nil || (!sure && !time.Now().Before(f.plays)) {
		return
	}
	f.settle(false)
	f.settle = nil
}

// lateInterruptWindow bounds how long after a turn ends an interrupt can still
// trim it; past that, playback has long finished.
const lateInterruptWindow = 10 * time.Minute

const heardUnset = -2

// interrupt stops the turn, remembering how much of it was heard.
func (t *turn) interrupt(heard int) {
	t.heard.CompareAndSwap(heardUnset, int64(heard))
	t.cancel(errInterrupted)
}

func (t *turn) delivered() int { return int(t.lastSeq.Load()) }

// New builds an agent. Call Start before Say.
func New(o Options) (*Agent, error) {
	if o.LLM == nil || o.Store == nil || o.Hub == nil {
		return nil, errors.New("agent: LLM, Store and Hub are required")
	}
	reg := o.Tools
	if reg == nil {
		var err error
		if reg, err = NewRegistry(); err != nil {
			return nil, err
		}
	}
	if _, ok := reg.Get(ThinkDeeper); !ok {
		if err := reg.Register(ThinkDeeperTool()); err != nil {
			return nil, err
		}
	}
	a := &Agent{
		conv: cmp.Or(o.ConversationID, ConversationID), system: o.System, written: o.Written,
		llm: o.LLM, store: o.Store, hub: o.Hub, synth: o.Synth, tools: reg, sources: o.Context, log: o.Logger,
		tamer: o.Tamer, loc: o.Location, deep: o.Deep,
		gestures: newGestureSource(o.AssetsDir), limits: o.Limits, now: o.Now, opener: o.Opener,
		checkIn: o.CheckIn,
	}
	a.SetModel(o.Chat)
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if a.loc == nil {
		a.loc = time.Local
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.limits.MaxRounds <= 0 {
		a.limits.MaxRounds = defaultMaxRounds
	}
	if a.limits.MaxCalls <= 0 {
		a.limits.MaxCalls = defaultMaxCalls
	}
	if a.limits.TurnTimeout <= 0 {
		a.limits.TurnTimeout = defaultTurnTimeout
	}
	if a.limits.ToolResultChars <= 0 {
		a.limits.ToolResultChars = defaultToolResultChars
	}
	if a.limits.ReplyTokens <= 0 {
		a.limits.ReplyTokens = defaultReplyTokens
	}
	if a.limits.Window == (Window{}) {
		a.limits.Window = DefaultWindow
	}
	return a, nil
}

// SetModel sets the model the next turns start with. When it is the deep
// model, think_deeper isn't offered: there's nothing deeper to switch to.
func (a *Agent) SetModel(m config.Model) { a.chat.Store(&m) }

// Model is the model turns start with.
func (a *Agent) Model() config.Model { return *a.chat.Load() }

// Start prepares the conversation. ctx bounds every turn's lifetime.
func (a *Agent) Start(ctx context.Context) error {
	if err := a.store.EnsureConversation(ctx, a.conv); err != nil {
		return fmt.Errorf("agent start: %w", err)
	}
	if err := a.store.AbortRunningTurns(ctx, a.conv); err != nil {
		return fmt.Errorf("agent start: %w", err)
	}
	a.baseCtx = ctx
	return nil
}

// Shutdown interrupts the running turn and waits for turns to wind down.
func (a *Agent) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	if a.cur != nil {
		a.cur.interrupt(a.cur.delivered())
	}
	a.mu.Unlock()
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("agent shutdown: %w", ctx.Err())
	}
}

// ConversationID returns the id of the agent's conversation.
func (a *Agent) ConversationID() string { return a.conv }

// Gestures lists the gesture names the character can perform.
// Timezone is the IANA zone the agent schedules and speaks in.
func (a *Agent) Timezone() string {
	if a.loc == nil {
		return "UTC"
	}
	return a.loc.String()
}

func (a *Agent) Gestures() []string {
	names, err := a.gestures.Names()
	if err != nil {
		a.log.Warn("cannot read gestures from the asset manifest", "error", err)
	}
	return names
}

// Say starts a turn for the given input. A running turn is interrupted first,
// as if the client had reported the last segment it was sent as heard. A quiet
// turn is answered in text only: no speech is synthesized.
func (a *Agent) Say(origin, inputID, text string, quiet bool) {
	a.SayNoted(origin, inputID, text, "", quiet)
}

// SayNoted is Say with a note the model reads after the owner's words and the owner doesn't see,
// such as where the files they sent with it are. It reports whether a turn started.
func (a *Agent) SayNoted(origin, inputID, text, note string, quiet bool) bool {
	if !a.llm.Configured() {
		a.hub.Broadcast(proto.NewError(proto.CodeLLMUnavailable,
			"I can't think right now: no language model is set up.", inputID))
		return false
	}
	ctx, cancel := context.WithCancelCause(a.baseCtx)
	a.start(&turn{id: newID("t_"), inputID: inputID, origin: origin, userText: text, quiet: quiet, note: note,
		ctx: ctx, cancel: cancel, done: make(chan struct{})})
	return true
}

// Prompt starts a quiet turn the owner didn't type, such as handing her a review of her work:
// note is what the model reads in their place, and nothing is shown for it. It returns the
// turn's id, or "" when no model is set up.
func (a *Agent) Prompt(origin, inputID, note string) string {
	if !a.llm.Configured() {
		a.hub.Broadcast(proto.NewError(proto.CodeLLMUnavailable,
			"I can't think right now: no language model is set up.", inputID))
		return ""
	}
	ctx, cancel := context.WithCancelCause(a.baseCtx)
	t := &turn{id: newID("t_"), inputID: inputID, origin: origin, quiet: true, note: note,
		ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.start(t)
	return t.id
}

// checkInNote is the owner's side of a check-in they started by tapping it.
const checkInNote = "(The owner tapped your evening check-in, \"How did today go?\")"

// CheckIn starts the evening check-in the owner tapped: a turn with nothing
// said, opened with the check-in's call, so her reply asks how the day went.
// day is the day the notice asked about ("" for today's). With nothing to
// start, it does nothing.
func (a *Agent) CheckIn(origin, inputID, day string, quiet bool) {
	if a.checkIn == nil {
		return
	}
	if !a.llm.Configured() {
		a.hub.Broadcast(proto.NewError(proto.CodeLLMUnavailable,
			"I can't think right now: no language model is set up.", inputID))
		return
	}
	op, ok := a.checkIn(a.baseCtx, day)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancelCause(a.baseCtx)
	a.start(&turn{id: newID("t_"), inputID: inputID, origin: origin, quiet: quiet, opening: &op, note: checkInNote,
		ctx: ctx, cancel: cancel, done: make(chan struct{})})
}

func (a *Agent) start(t *turn) {
	t.lastSeq.Store(-1)
	t.heard.Store(heardUnset)

	a.mu.Lock()
	prev := a.cur
	if prev != nil {
		prev.interrupt(prev.delivered())
		// A new turn starting is not a stop: the owner is usually replying to
		// what was said, so the previous turn's opener stays settled.
	}
	a.cur = t
	a.wg.Add(1)
	a.mu.Unlock()

	go a.runTurn(t, prev)
}

// Interrupt stops turn turnID (empty: the current turn). heardSeq is the last
// segment the user heard at least partly, or -1. An interrupt naming an older
// turn is ignored, so a late one can't cut a newer reply. If generation already
// finished and the client was still playing queued audio, the saved reply is
// trimmed to what was heard.
func (a *Agent) Interrupt(turnID string, heardSeq int) {
	// a.mu is held throughout so the next turn can't start (and save its user
	// message) while a finished reply is being rewritten: history stays in order.
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.cur
	if t == nil || (turnID != "" && t.id != turnID) {
		return
	}
	t.endMu.Lock()
	defer t.endMu.Unlock()
	if t.fin == nil {
		t.interrupt(heardSeq)
		return
	}
	t.fin.stopped(heardSeq < t.fin.kept)
	if heardSeq >= t.fin.kept || time.Since(t.fin.at) > lateInterruptWindow {
		return
	}
	a.trimFinished(t, heardSeq)
}

// Busy reports whether a turn is running (generation not finished).
func (a *Agent) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur == nil {
		return false
	}
	select {
	case <-a.cur.done:
		return false
	default:
		return true
	}
}

// Hush stops whatever she's saying because someone started talking (push-to-
// talk on any device). A running turn ends keeping what was delivered; a
// finished one that may still be playing somewhere is announced as
// interrupted so every client stops it. What was heard is unknown here, so
// nothing more is trimmed.
func (a *Agent) Hush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.cur
	if t == nil {
		return
	}
	t.endMu.Lock()
	defer t.endMu.Unlock()
	f := t.fin
	switch {
	case f == nil:
		t.interrupt(t.delivered())
	case !f.hushed && time.Since(f.at) <= lateInterruptWindow:
		f.hushed = true
		f.stopped(false)
		_, text := collectMessages(a.conv, a.written, t.id, f.rounds, f.segs, f.kept)
		a.hub.Broadcast(proto.NewTurnEnd(t.id, proto.StatusInterrupted, text, f.model, f.usage))
	}
}

// Stop ends the running turn, keeping everything it delivered. Unlike an
// interrupt, it never trims a turn that already finished: for written work,
// where nothing is "heard", what was written stays. A written turn is
// cancelled without a heard boundary, so a piece emitted while it stops is
// kept too; a spoken one keeps what was delivered when it was stopped.
func (a *Agent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.cur
	if t == nil {
		return
	}
	t.endMu.Lock()
	defer t.endMu.Unlock()
	switch {
	case t.fin != nil:
	case a.written:
		t.cancel(errInterrupted)
	default:
		t.interrupt(t.delivered())
	}
}

// testHookDuringTrim lets tests try to start a turn mid-trim.
var testHookDuringTrim func()

// trimFinished rewrites a completed turn's saved reply to end at seq heard and
// tells clients it was interrupted. Called with t.endMu held.
func (a *Agent) trimFinished(t *turn, heard int) {
	f := t.fin
	msgs, text := collectMessages(a.conv, a.written, t.id, f.rounds, f.segs, heard)
	if testHookDuringTrim != nil {
		testHookDuringTrim()
	}
	ctx, cancel := a.dbCtx()
	defer cancel()
	if err := a.store.ReplaceTurnReply(ctx, t.id, msgs); err != nil {
		a.log.Error("cannot trim interrupted reply", "turn_id", t.id, "error", err)
		return
	}
	if err := a.store.EndTurn(ctx, t.id, proto.StatusInterrupted, f.model, ""); err != nil {
		a.log.Error("cannot mark turn interrupted", "turn_id", t.id, "error", err)
	}
	f.kept, f.hushed = heard, true
	a.hub.Broadcast(proto.NewTurnEnd(t.id, proto.StatusInterrupted, text, f.model, f.usage))
	a.log.Info("trimmed finished turn", "turn_id", t.id, "kept", heard+1)
}

// Messages returns visible history, newest last. before is a message id (0 for
// the newest page).
func (a *Agent) Messages(ctx context.Context, before int64, limit int) ([]proto.HistoryMessage, error) {
	msgs, err := a.store.VisibleMessages(ctx, a.conv, before, limit)
	if err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	out := make([]proto.HistoryMessage, len(msgs))
	for i, m := range msgs {
		out[i] = proto.HistoryMessage{
			ID: strconv.FormatInt(m.ID, 10), Role: m.Role, Text: m.Text, TurnID: m.TurnID,
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return out, nil
}

func (a *Agent) vocabulary() perform.Vocabulary {
	return perform.Vocabulary{Gestures: a.Gestures(), Expressions: perform.DefaultExpressions}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail on supported platforms
	return prefix + hex.EncodeToString(b[:])
}

// dbCtx is a context for bookkeeping writes that must survive a cancelled turn.
func (a *Agent) dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(a.baseCtx), dbTimeout)
}
