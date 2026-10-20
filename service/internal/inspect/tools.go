package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// Tools returns her self-inspection tools and idea_log.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{
		{
			Name: "self_status",
			Description: "Check on yourself: Hekate's version and uptime, whether each part works (language model, voice, " +
				"listening, calendar, mail, notifications…), your routines' last runs, how many warnings and errors were " +
				"logged lately, and the last deploy. Use it first when asked whether something is wrong, or when a tool keeps failing.",
			Schema: json.RawMessage(`{"type":"object","properties":{}}`),
			Effect: agent.EffectRead,
			Label:  "Checking on myself",
			Run: func(ctx context.Context, _ json.RawMessage) (agent.Result, error) {
				out, err := s.Status(ctx)
				return agent.Result{Content: out}, err
			},
		},
		{
			Name: "self_logs",
			Description: "Read your recent log records, newest first: warnings by default. They only reach back to the last " +
				"restart. Use contains to look for one tool, account or error.",
			Schema: json.RawMessage(`{"type":"object","properties":{
				"level":{"type":"string","enum":["info","warn","error"],"description":"The lowest level shown; default warn."},
				"since_minutes":{"type":"integer","description":"How far back; default 1440 (a day)."},
				"contains":{"type":"string","description":"Only records mentioning this, any case."},
				"limit":{"type":"integer","description":"At most this many; default 20, up to 50."}
			}}`),
			Effect: agent.EffectRead,
			Label:  "Reading my logs",
			Run: func(_ context.Context, raw json.RawMessage) (agent.Result, error) {
				var q LogQuery
				if err := agent.DecodeArgs(raw, &q); err != nil {
					return agent.Result{}, err
				}
				out, err := s.Logs(q)
				return agent.Result{Content: out}, err
			},
		},
		{
			Name:        "self_usage",
			Description: "What your thinking has cost: requests, tokens and dollars over the last days, for conversation by model and for background work (mail sorting, routines) by source.",
			Schema: json.RawMessage(`{"type":"object","properties":{
				"days":{"type":"integer","description":"How many days back; default 7, up to 90."}
			}}`),
			Effect: agent.EffectRead,
			Label:  "Adding up my costs",
			Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
				var a struct {
					Days int `json:"days"`
				}
				if err := agent.DecodeArgs(raw, &a); err != nil {
					return agent.Result{}, err
				}
				out, err := s.Usage(ctx, a.Days)
				return agent.Result{Content: out}, err
			},
		},
		{
			Name: "self_activity",
			Description: "What you've been doing, newest first: your tool calls (kind tools, filter by tool or by status error " +
				"to find failures), your routines' runs (kind routines), or Hekate's deploys (kind deploys).",
			Schema: json.RawMessage(`{"type":"object","properties":{
				"kind":{"type":"string","enum":["tools","routines","deploys"],"description":"Default tools."},
				"hours":{"type":"integer","description":"How far back; default 24, up to 720."},
				"tool":{"type":"string","description":"tools: only this tool."},
				"status":{"type":"string","enum":["ok","error"],"description":"tools: only these."},
				"limit":{"type":"integer","description":"At most this many; default 20, up to 50."}
			}}`),
			Effect: agent.EffectRead,
			Label:  "Looking back at what I did",
			Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
				var q ActivityQuery
				if err := agent.DecodeArgs(raw, &q); err != nil {
					return agent.Result{}, err
				}
				out, err := s.Activity(ctx, q)
				return agent.Result{Content: out}, err
			},
		},
		{
			Name: "idea_log",
			Description: "Note an idea for improving yourself: a tool you wished you had, something that keeps failing, a " +
				"better way to do something. Ideas are just notes the owner can read in the menu; nothing gets built from " +
				"them. For a change you want built, use propose_change instead. At most 10 a day.",
			Schema: json.RawMessage(`{"type":"object","required":["idea"],"properties":{
				"idea":{"type":"string","description":"The idea in a sentence or two."},
				"kind":{"type":"string","enum":["tool","fix","improvement","other"]}
			}}`),
			Effect: agent.EffectLocal,
			Label:  "Noting an idea",
			Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
				var a struct {
					Idea string `json:"idea"`
					Kind string `json:"kind"`
				}
				if err := agent.DecodeArgs(raw, &a); err != nil {
					return agent.Result{}, err
				}
				idea, existed, err := s.LogIdea(ctx, a.Idea, a.Kind)
				switch {
				case errors.Is(err, ErrIdeaCap):
					return agent.Result{Content: ErrIdeaCap.Error() + "; keep this one in mind instead."}, nil
				case err != nil:
					return agent.Result{}, err
				case existed:
					return agent.Result{Content: fmt.Sprintf("Already noted as idea %s.", idea.ID)}, nil
				}
				return agent.Result{Content: fmt.Sprintf("Noted as idea %s.", idea.ID)}, nil
			},
		},
	}
}
