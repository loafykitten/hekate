package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/proto"
)

// Effect classifies what a tool can do, which later milestones use to decide
// whether it needs approval.
type Effect string

// Tool effects.
const (
	EffectRead     Effect = "read"     // looks things up
	EffectLocal    Effect = "local"    // changes Hekate's own data
	EffectExternal Effect = "external" // touches the outside world (mail, calendar)
)

// DefaultToolTimeout applies to tools that do not set their own.
const DefaultToolTimeout = 30 * time.Second

// Tool is something the model can call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON schema of the arguments object
	Effect      Effect
	// Label is the present-tense line shown while the tool runs, e.g.
	// "Checking your calendar". Defaults to "Working on it".
	Label   string
	Timeout time.Duration
	Run     func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result is what a tool hands back.
type Result struct {
	// Content goes back to the model.
	Content string
	// Card, if set, is shown on screen. Cards are filled by tools from the
	// database by id, never from model text.
	Card *Card
	// Cards are shown after Card, in order, for a result that shows several
	// things at once, such as the morning briefing.
	Cards []Card
	// Stored, if set, replaces Content in the saved conversation and the audit
	// log, for results that must not be kept, such as a mail body. The model
	// sees Content only for the rest of this turn.
	Stored string
}

// Card is something to show next to her; it is the wire shape from PROTOCOL.md.
type Card = proto.Card

// Registry holds the tools available to the model.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry validates and registers tools.
func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register adds a tool.
func (r *Registry) Register(t Tool) error {
	switch {
	case t.Name == "":
		return errors.New("tool without a name")
	case t.Run == nil:
		return fmt.Errorf("tool %q has no Run", t.Name)
	case !slices.Contains([]Effect{EffectRead, EffectLocal, EffectExternal}, t.Effect):
		return fmt.Errorf("tool %q has invalid effect %q", t.Name, t.Effect)
	case !json.Valid(t.Schema):
		return fmt.Errorf("tool %q has an invalid schema", t.Name)
	}
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("tool %q registered twice", t.Name)
	}
	if t.Timeout <= 0 {
		t.Timeout = DefaultToolTimeout
	}
	if t.Label == "" {
		t.Label = "Working on it"
	}
	r.tools[t.Name] = t
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Specs returns the tool definitions for a request, sorted by name so the
// prompt prefix stays cacheable, leaving out the named tools.
func (r *Registry) Specs(except ...string) []llm.Tool {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		if !slices.Contains(except, n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]llm.Tool, len(names))
	for i, n := range names {
		t := r.tools[n]
		out[i] = llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Schema}
	}
	return out
}

// ThinkDeeper is the name of the escalation tool the loop handles specially.
const ThinkDeeper = "think_deeper"

// ThinkDeeperTool lets the fast model hand the rest of the turn to the full
// model. The loop does the switching; Run only acknowledges.
func ThinkDeeperTool() Tool {
	return Tool{
		Name: ThinkDeeper,
		Description: "Switch to your deeper thinking mode for the rest of this reply. Call it before answering " +
			"when the request needs careful reasoning: weighing options, planning across several things, or a " +
			"sensitive personal moment. Do not call it for ordinary chat.",
		Schema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"Why this needs deeper thinking, in a few words."}},"required":["reason"]}`),
		Effect: EffectRead,
		Label:  "Thinking it over",
		Run: func(context.Context, json.RawMessage) (Result, error) {
			return Result{Content: "Deeper thinking is on. Carry on and answer now."}, nil
		},
	}
}

// DecodeArgs unmarshals a tool's arguments, rejecting unknown fields so a
// misspelt argument is reported back to the model instead of silently ignored.
func DecodeArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// ID is a record id in tool arguments. Models write 12 or "12" about equally
// often (the cards and results show ids as strings), so both are accepted.
type ID int64

// UnmarshalJSON implements json.Unmarshaler.
func (id *ID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("id %s is not a positive whole number", b)
	}
	*id = ID(n)
	return nil
}
