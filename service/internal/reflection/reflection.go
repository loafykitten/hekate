// Package reflection is Renamon's nightly reflection: she reads the day's
// conversation, what she did with her tools and everything she remembers, then
// tidies her memories (saves what she missed, merges duplicates, retires what's
// stale, schedules follow-ups) and writes her diary. Every change is recorded
// and can be undone from the menu.
package reflection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/store"
)

// JobName is the routine's name in job_runs and self_status.
const JobName = "reflection"

// DomainJournal is the invalidate domain of the diary and reflection reports.
const DomainJournal = "journal"

// When it runs, and how much it reads.
var runAt = jobs.Clock{Hour: 3, Minute: 30}

const (
	catchUp        = 8 * time.Hour
	defaultWindow  = 24 * time.Hour
	maxWindow      = 36 * time.Hour
	maxTranscript  = 60_000 // characters of conversation, newest kept
	maxActivity    = 80
	maxMemories    = 200
	maxSaves       = 10
	maxUpdates     = 15
	maxRetires     = 15
	maxDiary       = 4000
	maxReason      = 300
	modelMaxTokens = 6000
	runTimeout     = 8 * time.Minute
)

// Completer is the part of the LLM client reflection uses.
type Completer interface {
	Complete(ctx context.Context, req llm.Request) (string, *llm.Result, error)
	Configured() bool
}

// Options are the dependencies of a Service.
type Options struct {
	Store    *store.Store
	Memory   *memory.Service
	LLM      Completer
	Model    config.Model
	Tamer    string
	Location *time.Location
	Events   proto.Events
	Logger   *slog.Logger
	Now      func() time.Time // tests replace the clock
}

// Service runs reflections and serves the journal.
type Service struct {
	st     *store.Store
	db     *sql.DB
	mem    *memory.Service
	llm    Completer
	model  config.Model
	tamer  string
	loc    *time.Location
	events proto.Events
	log    *slog.Logger
	now    func() time.Time

	// mu keeps a reflection and an undo from interleaving.
	mu sync.Mutex
}

// New builds the service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Memory == nil || o.LLM == nil {
		return nil, errors.New("reflection: Store, Memory and LLM are required")
	}
	s := &Service{st: o.Store, db: o.Store.DB(), mem: o.Memory, llm: o.LLM, model: o.Model, tamer: o.Tamer,
		loc: o.Location, events: o.Events, log: o.Logger, now: o.Now}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.events == nil {
		s.events = proto.NopEvents{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.tamer == "" {
		s.tamer = "your Tamer"
	}
	return s, nil
}

// Job is the nightly routine.
func (s *Service) Job() jobs.Job {
	return jobs.Job{
		Name:    JobName,
		Next:    jobs.Daily(s.loc, catchUp, func(context.Context) (jobs.Clock, bool) { return runAt, true }),
		Run:     s.Reflect,
		Timeout: runTimeout,
	}
}

// Reflect runs one reflection over the conversation since the last one (a day
// at most, a day and a half after a long outage). With no conversation in that
// time there is nothing to reflect on, and nothing is recorded.
func (s *Service) Reflect(ctx context.Context) (string, error) {
	if !s.llm.Configured() {
		return "", errors.New("no language model is set up")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.now()
	start, err := s.windowStart(ctx, end)
	if err != nil {
		return "", err
	}
	forgets := s.mem.Forgets()
	in, err := s.gather(ctx, start, end)
	if err != nil {
		return "", err
	}
	if len(in.transcript) == 0 {
		return "", jobs.ErrSkip
	}
	text, res, err := s.llm.Complete(ctx, llm.Request{
		Model: s.model.Name, Effort: s.model.Effort, MaxTokens: modelMaxTokens,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: s.systemPrompt(in.day)},
			{Role: llm.RoleUser, Content: s.input(in)},
		},
	})
	agent.RecordBackgroundUsage(ctx, s.st, JobName, res, s.log)
	if err != nil {
		return "", fmt.Errorf("ask the model: %w", err)
	}
	p, err := parsePlan(text)
	if err != nil {
		return "", err
	}
	return s.apply(ctx, in, p, forgets)
}

// windowStart is where the last reflection stopped reading, bounded.
func (s *Service) windowStart(ctx context.Context, end time.Time) (time.Time, error) {
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(window_end) FROM reflections WHERE finished_at IS NOT NULL`).Scan(&last); err != nil {
		return time.Time{}, fmt.Errorf("find the last reflection: %w", err)
	}
	start := end.Add(-defaultWindow)
	if last.Valid {
		if t, err := time.Parse(time.RFC3339, last.String); err == nil {
			start = t
		}
	}
	if end.Sub(start) > maxWindow {
		start = end.Add(-maxWindow)
	}
	return start, nil
}

// stamp formats an instant for storage: UTC, fixed width, so strings sort.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
