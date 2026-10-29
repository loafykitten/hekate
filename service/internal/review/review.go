// Package review has a second model, from another family than hers, review Renamon's lab work
// in threads. It reads what the owner asked, what she said and what she ran, then checks her
// claims against the lab itself with read-only commands as the lab's reviewer account, and
// reports findings that each cite the commands that show them. Threads start reviews and hand
// her the findings (internal/threads); this package runs them and keeps them.
package review

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/store"
	"github.com/loafykitten/hekate/service/persona"
)

//go:embed prompt.md
var systemPrompt string

// Limits of one review.
const (
	MaxRounds  = 2  // the review of her fixes is the last
	MinBudget  = 12 // commands for the smallest piece of work
	MaxBudget  = 40 // and for the largest
	maxSteps   = 30 // model requests that may run commands
	runTimeout = 15 * time.Minute
	resultMax  = 8 << 10 // of one command's result, for the model
	storedOut  = 2 << 10 // of one command's output, kept for looking back
	labFails   = 3       // the lab failing this many commands in a row ends the review
)

// Inspector runs the reviewer's commands on the lab; lab.Service satisfies it.
type Inspector interface {
	ReviewerReady() bool
	Inspect(ctx context.Context, command string) (string, error)
}

// Options are the dependencies of a Reviewer.
type Options struct {
	Store   *store.Store
	LLM     *llm.Client
	Model   config.Model
	Lab     Inspector
	LabName string // what the owner's devices call the lab
	Logger  *slog.Logger
	Now     func() time.Time // tests replace the clock
}

// Reviewer runs reviews and keeps them.
type Reviewer struct {
	store  *store.Store
	db     *sql.DB
	llm    *llm.Client
	model  config.Model
	lab    Inspector
	system string
	rules  string // her lab's rules, as she reads them
	log    *slog.Logger
	now    func() time.Time
}

// New builds a Reviewer.
func New(o Options) (*Reviewer, error) {
	if o.Store == nil || o.LLM == nil || o.Lab == nil {
		return nil, errors.New("review: Store, LLM and Lab are required")
	}
	r := &Reviewer{store: o.Store, db: o.Store.DB(), llm: o.LLM, model: o.Model, lab: o.Lab, log: o.Logger, now: o.Now}
	name := o.LabName
	if name == "" {
		name = "hekate-lab"
	}
	r.system = strings.ReplaceAll(systemPrompt, "{{lab}}", name)
	r.rules = strings.NewReplacer("{{tamer}}", "the owner", "{{lab}}", name).Replace(persona.Section(persona.ThreadLab, "Your lab"))
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r, nil
}

// Ready reports whether reviews can run: a model to ask and a reviewer account on the lab.
func (r *Reviewer) Ready() bool { return r != nil && r.llm.Configured() && r.lab.ReviewerReady() }

// Model is the reviewer model's name.
func (r *Reviewer) Model() string { return r.model.Name }

// Request is what one review looks at.
type Request struct {
	Round    int
	Owner    []string // the owner's messages in the thread, oldest first
	Replies  []string // her replies, oldest first, the reviewed turn's last
	Actions  []string // what she did since the last review: her commands, the files she wrote, her searches
	Handover string   // what she said she's handing over as done, if she did
	Previous *Report  // the first round's report, for the review of her fixes
	Earlier  *Report  // the last review of earlier work in the thread, for the bigger picture
	Budget   int      // commands it may run; 0 is MaxBudget
}

// Budget is how many commands a review of changes many lab changes gets: enough to check each,
// within MinBudget and MaxBudget.
func Budget(changes int) int {
	return min(MaxBudget, max(MinBudget, MinBudget+2*changes))
}

// Report is what a review found.
type Report struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
	Verified []string  `json:"verified"`
}

// Finding is one problem, as the owner and she read it.
type Finding struct {
	Severity   string   `json:"severity"`
	Title      string   `json:"title"`
	Claim      string   `json:"claim"`
	Evidence   string   `json:"evidence"`
	Fix        string   `json:"fix"`
	Unverified bool     `json:"unverified"`
	Commands   []string `json:"commands"`
}

// Call is one command the reviewer ran, kept for looking back.
type Call struct {
	ID      int    `json:"id"`
	Command string `json:"command"`
	Output  string `json:"output"`
}

var inspectTool = llm.Tool{
	Name: "lab_inspect",
	Description: "Run a read-only shell command on the lab as the user reviewer (60 s limit) and see what it prints. " +
		"Allowed: reading files and listings (cat, ls, grep, find, stat, du, df, head, tail, jq, sed -n, awk), ss and ip, " +
		"journalctl, systemctl status, docker ps/inspect/logs/images/stats --no-stream, docker compose config/ps/ls, " +
		"docker manifest inspect, curl GET requests (the lab reaches the internet), sudo -n nft list ruleset and " +
		"sudo -n iptables -S, chained with |, ;, && or ||, output to /dev/null. Nothing that changes the lab. Each " +
		"result starts with [call N]: cite N as evidence.",
	Parameters: json.RawMessage(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"}}}`),
}

// Run reviews: it asks the model, runs the commands it asks for, and reads its report. The calls
// come back even when it fails partway, for keeping.
func (r *Reviewer) Run(ctx context.Context, req Request) (Report, []Call, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	if req.Budget <= 0 || req.Budget > MaxBudget {
		req.Budget = MaxBudget
	}
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: r.system}, {Role: llm.RoleUser, Content: userMessage(req, r.rules)}}
	var calls []Call
	fails := 0
	final := ""
	finished := false
	for step := 0; step < min(maxSteps, req.Budget) && !finished; step++ {
		text, toolCalls, err := r.complete(ctx, msgs, true)
		if err != nil {
			return Report{}, calls, err
		}
		if len(toolCalls) == 0 {
			final, finished = text, true
			break
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: toolCalls})
		for _, tc := range toolCalls {
			content, call, err := r.inspect(ctx, tc, len(calls), req.Budget)
			switch {
			case err != nil && ctx.Err() != nil:
				return Report{}, calls, fmt.Errorf("review: %w", ctx.Err())
			case err != nil:
				fails++
				if fails >= labFails {
					return Report{}, calls, fmt.Errorf("the lab didn't answer: %w", err)
				}
				content = "Not run: " + err.Error()
			default:
				fails = 0
			}
			if call != nil {
				calls = append(calls, *call)
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: content})
		}
	}
	if !finished {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "That's all the looking there's room for. Write your JSON report now."})
		text, _, err := r.complete(ctx, msgs, false)
		if err != nil {
			return Report{}, calls, err
		}
		final = text
	}
	rep, err := parseReport(final, calls)
	if err != nil {
		r.log.Warn("review: unreadable report, asking again", "error", err)
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: final},
			llm.Message{Role: llm.RoleUser, Content: "That report couldn't be read: " + err.Error() + ". Send only the JSON object, nothing else."})
		text, _, cerr := r.complete(ctx, msgs, false)
		if cerr != nil {
			return Report{}, calls, cerr
		}
		if rep, err = parseReport(text, calls); err != nil {
			return Report{}, calls, fmt.Errorf("the reviewer's report couldn't be read: %w", err)
		}
	}
	return rep, calls, nil
}

// inspect runs one command the model asked for. A command that ran gets the next id and comes
// back as a Call; one that was refused or couldn't be read doesn't.
func (r *Reviewer) inspect(ctx context.Context, tc llm.ToolCall, ran, budget int) (string, *Call, error) {
	if tc.Name != inspectTool.Name {
		return fmt.Sprintf("Not run: there's no tool %q; use %s.", tc.Name, inspectTool.Name), nil, nil
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(tc.Arguments), &a); err != nil || strings.TrimSpace(a.Command) == "" {
		return `Not run: the arguments must be {"command": "..."}.`, nil, nil
	}
	if ran >= budget {
		return fmt.Sprintf("Not run: all %d commands are used. Write your report now.", budget), nil, nil
	}
	out, err := r.lab.Inspect(ctx, a.Command)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // the lab's errors say what failed
	}
	if strings.HasPrefix(out, "Not run: ") {
		return out, nil, nil
	}
	id := ran + 1
	return fmt.Sprintf("[call %d] %s", id, clipRunes(out, resultMax)),
		&Call{ID: id, Command: a.Command, Output: clipRunes(out, storedOut)}, nil
}

// complete runs one request to the end and returns its text and tool calls.
func (r *Reviewer) complete(ctx context.Context, msgs []llm.Message, tools bool) (string, []llm.ToolCall, error) {
	req := llm.Request{Model: r.model.Name, Effort: r.model.Effort, Messages: msgs}
	if tools {
		req.Tools = []llm.Tool{inspectTool}
	}
	stream, err := r.llm.Stream(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("ask the reviewer: %w", err)
	}
	defer func() { _ = stream.Close() }()
	var text strings.Builder
	var calls []llm.ToolCall
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return text.String(), calls, nil
		}
		if err != nil {
			return "", nil, fmt.Errorf("read the reviewer's answer: %w", err)
		}
		switch ev.Kind {
		case llm.EventText:
			text.WriteString(ev.Text)
		case llm.EventToolCall:
			calls = append(calls, ev.ToolCall)
		case llm.EventDone:
			agent.RecordBackgroundUsage(ctx, r.store, "review", ev.Result, r.log)
		}
	}
}

// clipRunes cuts s to at most n bytes on a rune boundary, marking the cut.
func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[... cut ...]"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
