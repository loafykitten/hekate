package routines

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// Tools returns routine_settings.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{{
		Name: "routine_settings",
		Description: "See or change your routines: check-in nudges (when something needs the owner: an event to leave for, " +
			"an overdue task, mail that needs them, a follow-up), the waking hours they're limited to, and the morning " +
			"briefing you write and give when the app opens. Change them only when the owner asks, e.g. \"stop nudging me\", " +
			"\"not before nine\" or \"brief me at seven\". Without arguments it just reports.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"heartbeat":{"type":"boolean","description":"Check-in nudges on or off."},
			"waking_start":{"type":"string","description":"HH:MM, 24-hour, local."},
			"waking_end":{"type":"string","description":"HH:MM, 24-hour, local."},
			"briefing":{"type":"boolean","description":"The morning briefing on or off."},
			"briefing_at":{"type":"string","description":"HH:MM, 24-hour, local: when the briefing is written and sent."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Adjusting my routines",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var p Patch
			if err := agent.DecodeArgs(raw, &p); err != nil {
				return agent.Result{}, err
			}
			var st Settings
			var err error
			if p == (Patch{}) {
				st, err = s.Settings(ctx)
			} else {
				st, err = s.Update(ctx, p)
			}
			if err != nil {
				return agent.Result{}, err
			}
			onOff := func(b bool) string {
				if b {
					return "on"
				}
				return "off"
			}
			return agent.Result{Content: fmt.Sprintf("Check-in nudges are %s, between %s and %s. The morning briefing is %s, at %s.",
				onOff(st.Heartbeat), st.WakingStart, st.WakingEnd, onOff(st.Briefing), st.BriefingAt)}, nil
		},
	}}
}
