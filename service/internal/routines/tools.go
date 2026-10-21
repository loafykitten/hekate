package routines

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/loafykitten/hekate/service/internal/agent"
)

// Tools returns routine_settings, morning_briefing and evening_checkin.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{{
		Name: "routine_settings",
		Description: "See or change your routines: nudges (when something needs the owner: an event to leave for, an overdue " +
			"task, mail that needs them, a follow-up) and the waking hours they're limited to, and the evening check-in (one " +
			"notice a day asking how it went) and its time. Change them only when the owner asks, e.g. \"stop nudging me\", " +
			"\"not before nine\" or \"ask about my day at eleven\". Without arguments it just reports.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"heartbeat":{"type":"boolean","description":"Nudges on or off."},
			"waking_start":{"type":"string","description":"HH:MM, 24-hour, local."},
			"waking_end":{"type":"string","description":"HH:MM, 24-hour, local."},
			"checkin":{"type":"boolean","description":"The evening check-in on or off."},
			"checkin_at":{"type":"string","description":"When the check-in asks, HH:MM, 24-hour, local."}
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
			return agent.Result{Content: fmt.Sprintf("Nudges are %s, between %s and %s. The evening check-in is %s, at %s.",
				onOff(st.Heartbeat), st.WakingStart, st.WakingEnd, onOff(st.CheckIn), st.CheckInAt)}, nil
		},
	}, s.briefingTool(), s.checkInTool()}
}
