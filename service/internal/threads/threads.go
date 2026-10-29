// Package threads holds the owner's threads: conversations of their own for
// longer work (research, lab work), beside the one voice conversation. Each
// runs its own agent with work tools, a written-work prompt, a long turn
// budget and a model of its own (GLM 5.3 unless switched). Their events reach
// the clients wrapped as thread_event, and Renamon reaches them from the main
// conversation through her thread tools.
package threads

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/review"
	"github.com/loafykitten/hekate/service/internal/server"
	"github.com/loafykitten/hekate/service/internal/store"
)

// Models a thread can use.
const (
	ModelDeep = "deep" // the deep model (GLM 5.3): the default
	ModelChat = "chat" // the everyday model (GLM 5.3 Flash)
)

// Who opened a thread.
const (
	OriginOwner   = "owner"
	OriginRenamon = "renamon"
)

// Limits.
const (
	maxRunning     = 3
	maxTitle       = 80
	autoTitle      = 60
	maxPreview     = 160
	placeholder    = "New thread"
	notifyAfter    = time.Minute // a turn this long ends with a notification: the owner has likely looked away
	reviewQuiet    = 10 * time.Minute
	notifyBody     = 240
	defaultHistory = 50
	maxHistory     = 100
)

// Limits are a thread turn's: long, since the owner isn't waiting on a voice.
var Limits = agent.Limits{
	MaxRounds: 40, MaxCalls: 80, TurnTimeout: 30 * time.Minute, ToolResultChars: 12000,
	// The deep model reasons at length before it writes, and its replies are long: 4096 (the
	// voice's) once ran out before it wrote a word.
	ReplyTokens: 32768,
	Window:      agent.Window{MaxMessages: 400, MaxChars: 200000, LowMessages: 200, LowChars: 100000},
}

// Thread is the API shape from PROTOCOL.md.
type Thread struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	ModelName string `json:"model_name"`
	Origin    string `json:"origin"`
	Running   bool   `json:"running"`
	Reviewing bool   `json:"reviewing"`
	Archived  bool   `json:"archived"`
	Preview   string `json:"preview"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Pusher delivers web push; push.Service satisfies it.
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Options are the dependencies of a Service.
type Options struct {
	Store *store.Store
	LLM   *llm.Client
	Deep  config.Model // the default
	Chat  config.Model
	// Tools are what a thread's agent can use; Context its prompt's sources.
	Tools   []agent.Tool
	Context []agent.Source
	// System builds a thread's system prompt from the owner's name.
	System func(tamer string) string
	// Hub carries the wrapped events to every client.
	Hub    agent.Hub
	Events proto.Events
	Push   Pusher // nil: in-app notifications only
	// Reviewer reviews her lab work after a turn that changed the lab; nil: no reviews.
	Reviewer *review.Reviewer
	// Lab carries the files passed in threads, and gives her lab_share_file; nil: no files.
	Lab Lab
	// Vision looks at images in the lab for her (look_at_image), with the lab; zero: no looking.
	Vision config.Model
	// ReviewQuiet is how long a thread stays quiet before lab changes she didn't hand over are
	// reviewed anyway; zero: reviewQuiet.
	ReviewQuiet time.Duration
	Tamer       string
	Location    *time.Location
	Logger      *slog.Logger
	Now         func() time.Time // tests replace the clock
	Limits      agent.Limits     // zero: Limits
}

// Service keeps the threads and their agents.
type Service struct {
	store      *store.Store
	db         *sql.DB
	opts       Options
	hub        agent.Hub
	events     proto.Events
	pusher     Pusher
	log        *slog.Logger
	loc        *time.Location
	now        func() time.Time
	limits     agent.Limits
	tools      []agent.Tool      // a thread agent's
	labels     map[string]string // tool name -> its label, for steps
	quietAfter time.Duration
	baseCtx    context.Context //nolint:containedctx // the service's lifetime, set by Start

	mu    sync.Mutex // guards convs, and makes "may this thread start?" and starting it one step
	convs map[string]*conv

	reviewsWG sync.WaitGroup // reviews in progress
}

// New builds the service. Call Start before use.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.LLM == nil || o.Hub == nil || o.System == nil {
		return nil, errors.New("threads: Store, LLM, Hub and System are required")
	}
	s := &Service{store: o.Store, db: o.Store.DB(), opts: o, hub: o.Hub, events: o.Events, pusher: o.Push, log: o.Logger,
		loc: o.Location, now: o.Now, limits: o.Limits, labels: map[string]string{}, convs: map[string]*conv{}}
	if s.events == nil {
		s.events = proto.NopEvents{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.limits == (agent.Limits{}) {
		s.limits = Limits
	}
	s.quietAfter = cmp.Or(o.ReviewQuiet, reviewQuiet)
	s.tools = slices.Clone(o.Tools)
	if o.Reviewer != nil {
		s.tools = append(s.tools, s.finishTool())
	}
	if o.Lab != nil {
		s.tools = append(s.tools, s.shareTool())
		if o.Vision.Name != "" {
			s.tools = append(s.tools, s.lookTool())
		}
	}
	if _, err := agent.NewRegistry(s.tools...); err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	for _, t := range s.tools {
		s.labels[t.Name] = t.Label
	}
	return s, nil
}

// Start marks turns left running by the last shutdown as failed. ctx bounds
// every thread turn's lifetime.
func (s *Service) Start(ctx context.Context) error {
	ids, err := s.ids(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.store.AbortRunningTurns(ctx, id); err != nil {
			return fmt.Errorf("threads start: %w", err)
		}
	}
	if s.opts.Reviewer != nil {
		if err := s.opts.Reviewer.AbortRunning(ctx); err != nil {
			return fmt.Errorf("threads start: %w", err)
		}
	}
	s.baseCtx = ctx
	return s.armPending(ctx)
}

// Shutdown stops the running thread turns and waits for them to wind down.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	convs := make([]*conv, 0, len(s.convs))
	for _, c := range s.convs {
		convs = append(convs, c)
	}
	s.mu.Unlock()
	var errs []error
	for _, c := range convs {
		c.stopQuiet()
		c.mu.Lock()
		if c.review != nil {
			c.review.cancel(errShutdown)
		}
		c.mu.Unlock()
		errs = append(errs, c.ag.Shutdown(ctx))
	}
	done := make(chan struct{})
	go func() { s.reviewsWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, fmt.Errorf("reviews still running: %w", ctx.Err()))
	}
	return errors.Join(errs...)
}

func (s *Service) ids(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM threads`)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list threads: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	return ids, nil
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Service) display(stored string) string {
	t, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		return stored
	}
	return t.In(s.loc).Format(time.RFC3339)
}

func (s *Service) modelFor(name string) config.Model {
	if name == ModelChat {
		return s.opts.Chat
	}
	return s.opts.Deep
}

func notFound(id string) error { return httpx.NotFound("there is no thread %q", id) }

const columns = `id, title, model, origin, archived, preview, created_at, updated_at`

func (s *Service) scan(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var created, updated string
	if err := row.Scan(&t.ID, &t.Title, &t.Model, &t.Origin, &t.Archived, &t.Preview, &created, &updated); err != nil {
		return Thread{}, err //nolint:wrapcheck // callers tell sql.ErrNoRows apart
	}
	t.ModelName = s.modelFor(t.Model).Name
	t.CreatedAt, t.UpdatedAt = s.display(created), s.display(updated)
	t.Running, t.Reviewing = s.running(t.ID)
	return t, nil
}

// Get returns one thread.
func (s *Service) Get(ctx context.Context, id string) (Thread, error) {
	t, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM threads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, notFound(id)
	}
	if err != nil {
		return Thread{}, fmt.Errorf("get thread: %w", err)
	}
	return t, nil
}

// List returns the active threads (or the archived ones), most recently
// updated first.
func (s *Service) List(ctx context.Context, archived bool) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM threads WHERE archived = ? ORDER BY updated_at DESC, id`, archived)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Thread{}
	for rows.Next() {
		t, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list threads: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	return out, nil
}

// Create opens a thread. An empty title is the placeholder until the first
// message names it; an empty model is the deep one.
func (s *Service) Create(ctx context.Context, title, model, origin string) (Thread, error) {
	title = strings.Join(strings.Fields(title), " ")
	titled := title != ""
	if !titled {
		title = placeholder
	}
	if utf8.RuneCountInString(title) > maxTitle {
		return Thread{}, httpx.BadRequest("a title is at most %d characters", maxTitle)
	}
	if model == "" {
		model = ModelDeep
	}
	if model != ModelDeep && model != ModelChat {
		return Thread{}, httpx.BadRequest("model must be %q or %q", ModelDeep, ModelChat)
	}
	id := newID()
	now := stamp(s.now())
	if err := s.store.EnsureConversation(ctx, id); err != nil {
		return Thread{}, fmt.Errorf("create thread: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO threads (id, title, titled, model, origin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, title, titled, model, origin, now, now); err != nil {
		return Thread{}, fmt.Errorf("create thread: %w", err)
	}
	s.events.Invalidate("threads")
	return s.Get(ctx, id)
}

// Patch is a change to a thread; nil fields stay as they are.
type Patch struct {
	Title    *string `json:"title"`
	Model    *string `json:"model"`
	Archived *bool   `json:"archived"`
}

// Update renames a thread, switches its model or archives it. Archiving stops a
// running turn.
func (s *Service) Update(ctx context.Context, id string, p Patch) (Thread, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return Thread{}, err
	}
	sets, args := []string{}, []any{}
	if p.Title != nil {
		title := strings.Join(strings.Fields(*p.Title), " ")
		if n := utf8.RuneCountInString(title); n == 0 || n > maxTitle {
			return Thread{}, httpx.BadRequest("a title is 1 to %d characters", maxTitle)
		}
		sets, args = append(sets, "title = ?", "titled = 1"), append(args, title)
	}
	if p.Model != nil {
		if *p.Model != ModelDeep && *p.Model != ModelChat {
			return Thread{}, httpx.BadRequest("model must be %q or %q", ModelDeep, ModelChat)
		}
		sets, args = append(sets, "model = ?"), append(args, *p.Model)
	}
	if p.Archived != nil {
		sets, args = append(sets, "archived = ?"), append(args, *p.Archived)
	}
	if len(sets) == 0 {
		return s.Get(ctx, id)
	}
	sets, args = append(sets, "updated_at = ?"), append(args, stamp(s.now()), id)
	//nolint:gosec // sets are fixed column assignments; values go through args
	if _, err := s.db.ExecContext(ctx, `UPDATE threads SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return Thread{}, fmt.Errorf("update thread: %w", err)
	}
	s.mu.Lock()
	c := s.convs[id]
	s.mu.Unlock()
	if c != nil {
		if p.Model != nil {
			c.ag.SetModel(s.modelFor(*p.Model))
		}
		if p.Archived != nil && *p.Archived {
			c.ag.Stop()
			c.stopReview()
			c.stopQuiet()
		}
	}
	s.events.Invalidate("threads")
	return s.Get(ctx, id)
}

// running reports whether a thread is working: a turn, or a review of its work, in progress.
// reviewing is the review alone.
func (s *Service) running(id string) (running, reviewing bool) {
	s.mu.Lock()
	c := s.convs[id]
	s.mu.Unlock()
	if c == nil {
		return false, false
	}
	reviewing = c.reviewing()
	return reviewing || c.ag.Busy(), reviewing
}

// Input returns a thread's conversation for input; archived threads take none.
// It satisfies server.Threads.
func (s *Service) Input(ctx context.Context, id string) (server.Input, error) {
	return s.conv(ctx, id)
}

// conv returns the thread's agent, building it on first use.
func (s *Service) conv(ctx context.Context, id string) (*conv, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Archived {
		return nil, httpx.Conflict("thread %q is archived", id)
	}
	if s.baseCtx == nil {
		return nil, errors.New("threads: not started")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[id]; c != nil {
		return c, nil
	}
	c := &conv{s: s, id: id, starts: map[string]turnInfo{}, sending: map[string][]proto.ThreadFile{}}
	reg, err := agent.NewRegistry(s.tools...)
	if err != nil {
		return nil, fmt.Errorf("thread tools: %w", err)
	}
	ag, err := agent.New(agent.Options{
		ConversationID: id, System: s.opts.System, Written: true, LLM: s.opts.LLM, Store: s.store, Hub: threadHub{c},
		Tools: reg, Context: s.opts.Context, Logger: s.log.With("thread", id), Tamer: s.opts.Tamer,
		Location: s.loc, Chat: s.modelFor(t.Model), Deep: s.opts.Deep, Limits: s.limits,
	})
	if err != nil {
		return nil, fmt.Errorf("thread agent: %w", err)
	}
	if err := ag.Start(s.baseCtx); err != nil {
		return nil, fmt.Errorf("thread agent: %w", err)
	}
	c.ag = ag
	s.convs[id] = c
	return c, nil
}

// conv is one thread's agent, as socket input and for her tools.
type conv struct {
	s  *Service
	id string
	ag *agent.Agent

	mu      sync.Mutex
	starts  map[string]turnInfo           // running turns, by id
	review  *reviewing                    // a review of its work in progress
	quiet   *time.Timer                   // reviews her unreviewed lab changes if the thread stays quiet
	sending map[string][]proto.ThreadFile // files sent with input whose turn hasn't started, by input id
}

// turnInfo is what's needed when a turn ends.
type turnInfo struct {
	at     time.Time
	origin string
}

// ConversationID implements server.Input.
func (c *conv) ConversationID() string { return c.id }

// Say starts a turn, unless the thread is still working or three others are:
// a thread doesn't cut its own work short for new input, so that's an error
// for the sender, as is a fourth thread running at once.
func (c *conv) Say(origin, inputID, text string, _ bool) {
	if code, msg := c.say(origin, inputID, text, nil); msg != "" {
		c.s.hub.Broadcast(proto.NewThreadEvent(c.id, proto.NewError(code, msg, inputID)))
	}
}

// What a thread says when it can't take input now.
const (
	stillWorking = "I'm still working on your last message. Stop me first, or wait for me to finish."
	inReview     = "My last piece of work is still being reviewed. Stop the review, or wait for it to finish."
	tooMany      = "Three threads are already working. Let one finish first."
)

// say starts the turn, with the files sent with it, or says why it can't (an error code and message).
func (c *conv) say(origin, inputID, text string, fileIDs []string) (code, msg string) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ag.Busy() {
		return proto.CodeBusy, stillWorking
	}
	if c.reviewing() {
		return proto.CodeBusy, inReview
	}
	running := 0
	for _, other := range s.convs {
		if other.ag.Busy() || other.reviewing() {
			running++
		}
	}
	if running >= maxRunning {
		return proto.CodeBusy, tooMany
	}
	note := ""
	if len(fileIDs) > 0 {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 5*time.Second)
		defer cancel()
		fs, err := c.unsent(ctx, fileIDs)
		if err != nil {
			s.log.Warn("threads: files refused", "thread", c.id, "error", err)
			return proto.CodeBadMessage, "Those files can't be sent: upload them to this thread again."
		}
		note = filesNote(s.opts.Tamer, fs)
		c.mu.Lock()
		c.sending[inputID] = fs
		c.mu.Unlock()
	}
	s.nameFromFirst(c.id, text)
	if !c.ag.SayNoted(origin, inputID, text, note, true) { // quiet: threads are text only
		c.dropSending(inputID)
	}
	return "", ""
}

// nameFromFirst titles a placeholder thread with the start of its first message.
func (s *Service) nameFromFirst(id, text string) {
	title := strings.Join(strings.Fields(text), " ")
	if r := []rune(title); len(r) > autoTitle {
		cut := string(r[:autoTitle])
		if i := strings.LastIndexByte(cut, ' '); i > autoTitle/2 {
			cut = cut[:i]
		}
		title = cut + "…"
	}
	if title == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `UPDATE threads SET title = ?, titled = 1 WHERE id = ? AND titled = 0`, title, id); err != nil {
		s.log.Warn("threads: can't name a thread", "thread", id, "error", err)
	}
}

// Interrupt implements server.Input: it stops the running turn, keeping what
// she wrote, or the review of her work. Nothing in a thread is "heard", so
// heard_seq is ignored, and a finished reply is never trimmed.
func (c *conv) Interrupt(string, int) {
	c.ag.Stop()
	c.stopReview()
}

// Hush implements server.Input. A thread has no voice to stop, and starting to
// talk in one doesn't end its work.
func (c *conv) Hush() {}

// threadHub wraps a thread's events for the clients and keeps the thread's
// record as its turns start and end.
type threadHub struct{ c *conv }

func (h threadHub) Broadcast(msg any) {
	switch m := msg.(type) {
	case proto.TurnStart:
		h.c.stopQuiet() // she's working again: her next turn's end decides
		h.c.claimFiles(&m)
		msg = m
	case proto.Error:
		// The turn didn't start: its files can be sent again.
		h.c.dropSending(m.InputID)
	}
	h.c.s.hub.Broadcast(proto.NewThreadEvent(h.c.id, msg))
	switch m := msg.(type) {
	case proto.TurnStart:
		h.c.mu.Lock()
		h.c.starts[m.TurnID] = turnInfo{at: h.c.s.now(), origin: m.Origin}
		h.c.mu.Unlock()
		h.c.s.touched(h.c.id, nil)
	case proto.TurnEnd:
		h.c.mu.Lock()
		info, ok := h.c.starts[m.TurnID]
		delete(h.c.starts, m.TurnID)
		h.c.mu.Unlock()
		text := m.Text
		h.c.s.touched(h.c.id, &text)
		// A review that follows is part of the work: the owner hears when it ends instead.
		if !h.c.maybeReview(m, info) && ok {
			h.c.s.ended(h.c.id, info, m)
		}
	}
}

// touched records activity on a thread, and her latest reply when given.
func (s *Service) touched(id string, reply *string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 5*time.Second)
	defer cancel()
	var err error
	if reply != nil && strings.TrimSpace(*reply) != "" {
		_, err = s.db.ExecContext(ctx, `UPDATE threads SET preview = ?, updated_at = ? WHERE id = ?`,
			clip(*reply, maxPreview), stamp(s.now()), id)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE threads SET updated_at = ? WHERE id = ?`, stamp(s.now()), id)
	}
	if err != nil {
		s.log.Warn("threads: can't record activity", "thread", id, "error", err)
	}
	s.events.Invalidate("threads")
}

// ended tells the owner a turn finished when they likely aren't watching: one
// Renamon started from the main conversation, or one that ran a while.
func (s *Service) ended(id string, info turnInfo, m proto.TurnEnd) {
	if m.Status == proto.StatusInterrupted ||
		(info.origin != OriginRenamon && info.origin != proto.OriginReview && s.now().Sub(info.at) < notifyAfter) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 30*time.Second)
	defer cancel()
	// Straight from the table: this runs inside the agent's end of turn, which
	// holds its locks, so nothing here may ask the agent anything (Busy).
	var title string
	if err := s.db.QueryRowContext(ctx, `SELECT title FROM threads WHERE id = ?`, id).Scan(&title); err != nil {
		s.log.Warn("threads: can't read a thread to notify about it", "thread", id, "error", err)
		return
	}
	body := clip(m.Text, notifyBody)
	if m.Status == proto.StatusError {
		body = "Something went wrong; open the thread to see."
	}
	s.events.Notify(proto.NewNotify(id, "thread", title, body, nil))
	if s.pusher != nil {
		if _, err := s.pusher.Send(ctx, push.Payload{Title: title, Body: body, URL: "/?thread=" + id, Tag: "thread-" + id}, push.UrgencyNormal); err != nil {
			s.log.Warn("threads: push failed", "thread", id, "error", err)
		}
	}
}

// clip flattens text to one line of at most n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail on supported platforms
	return "th_" + hex.EncodeToString(b[:])
}
