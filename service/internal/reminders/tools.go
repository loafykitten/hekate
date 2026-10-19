package reminders

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/httpx"
)

const maxCardItems = 20

// Tools returns the model's reminder tools.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{s.createTool(), s.listTool(), s.updateTool(), s.snoozeTool(), s.cancelTool()}
}

const atHelp = "Local time unless it has an offset: 2026-10-20T09:00, or RFC 3339. A time of day is required."

func (s *Service) createTool() agent.Tool {
	return agent.Tool{
		Name: "reminder_create",
		Description: "Set a reminder. It notifies the owner on their devices at the time. Give `at` for a clock time, or " +
			"`in_minutes` for \"in 20 minutes\" (prefer it for anything relative and short). Times in the past are refused. " +
			"For repeating reminders also give `rrule`, e.g. FREQ=DAILY or FREQ=WEEKLY;BYDAY=MO.",
		Schema: json.RawMessage(`{"type":"object","required":["text"],"properties":{
			"text":{"type":"string","description":"What to remind them of, phrased as the notification, e.g. \"Stretch\"."},
			"at":{"type":"string","description":"` + atHelp + `"},
			"in_minutes":{"type":"integer","minimum":1,"description":"Minutes from now. Use instead of at."},
			"rrule":{"type":"string","description":"RFC 5545 RRULE for a repeating reminder. The first time is still given by at or in_minutes."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Setting a reminder",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Text      string `json:"text"`
				At        string `json:"at"`
				InMinutes int    `json:"in_minutes"`
				RRule     string `json:"rrule"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			var (
				r   Reminder
				err error
			)
			switch {
			case a.At != "" && a.InMinutes != 0:
				err = httpx.BadRequest("give either at or in_minutes, not both")
			case a.At != "":
				r, err = s.Create(ctx, a.Text, a.At, a.RRule)
			case a.InMinutes != 0:
				r, err = s.CreateIn(ctx, a.Text, a.InMinutes, a.RRule)
			default:
				err = httpx.BadRequest("give at or in_minutes")
			}
			if err != nil {
				return agent.Result{}, err
			}
			return s.result(ctx, "Reminder set: "+oneLine(r), r)
		},
	}
}

func (s *Service) listTool() agent.Tool {
	return agent.Tool{
		Name:        "reminder_list",
		Description: "Look up reminders. Default is the upcoming ones, soonest first; `past` shows ones that already fired or were cancelled.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"status":{"type":"string","enum":["upcoming","past","all"],"description":"Default upcoming."}}}`),
		Effect: agent.EffectRead,
		Label:  "Checking your reminders",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Status string `json:"status"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			list, err := s.List(ctx, a.Status)
			if err != nil {
				return agent.Result{}, err
			}
			if len(list) == 0 {
				content := "No upcoming reminders."
				if a.Status == ListPast {
					content = "No past reminders."
				}
				card, err := reminderCard("Reminders", nil, nil)
				return agent.Result{Content: content, Card: card}, err
			}
			var lines []string
			for _, r := range list {
				lines = append(lines, "- "+oneLine(r))
			}
			if len(list) > maxCardItems {
				list = list[:maxCardItems]
			}
			card, err := reminderCard("Reminders", list, nil)
			return agent.Result{Content: strings.Join(lines, "\n"), Card: card}, err
		},
	}
}

func (s *Service) updateTool() agent.Tool {
	return agent.Tool{
		Name: "reminder_update",
		Description: "Change a reminder: its text, its time (`at` or `in_minutes`; for a repeating reminder this moves every " +
			"future occurrence) or its repeat rule (empty string stops repeating). To put off just this once, use reminder_snooze.",
		Schema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id":{"type":"string","description":"The reminder id, as shown in reminder results."},
			"text":{"type":"string"},
			"at":{"type":"string","description":"` + atHelp + `"},
			"in_minutes":{"type":"integer","minimum":1},
			"rrule":{"type":"string","description":"RFC 5545 RRULE. Empty string stops repeating."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Changing a reminder",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				ID        agent.ID `json:"id"`
				Text      *string  `json:"text"`
				At        *string  `json:"at"`
				InMinutes *int     `json:"in_minutes"`
				RRule     *string  `json:"rrule"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ID == 0 {
				return agent.Result{}, httpx.BadRequest("id is required")
			}
			r, err := s.Update(ctx, int64(a.ID), Patch{Text: a.Text, At: a.At, InMinutes: a.InMinutes, RRule: a.RRule})
			if err != nil {
				return agent.Result{}, err
			}
			return s.result(ctx, "Reminder updated: "+oneLine(r), r)
		},
	}
}

func (s *Service) snoozeTool() agent.Tool {
	return agent.Tool{
		Name: "reminder_snooze",
		Description: "Put a reminder off for a while (\"remind me again in ten minutes\"). A repeating reminder fires again " +
			"after the snooze, then keeps its usual schedule.",
		Schema: json.RawMessage(`{"type":"object","required":["id","minutes"],"properties":{
			"id":{"type":"string","description":"The reminder id, as shown in reminder results."},
			"minutes":{"type":"integer","minimum":1}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Snoozing the reminder",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				ID      agent.ID `json:"id"`
				Minutes int      `json:"minutes"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ID == 0 {
				return agent.Result{}, httpx.BadRequest("id is required")
			}
			r, err := s.Snooze(ctx, int64(a.ID), a.Minutes)
			if err != nil {
				return agent.Result{}, err
			}
			return s.result(ctx, "Reminder snoozed: "+oneLine(r), r)
		},
	}
}

func (s *Service) cancelTool() agent.Tool {
	return agent.Tool{
		Name:        "reminder_cancel",
		Description: "Cancel a reminder so it never fires. A repeating reminder stops for good.",
		Schema:      json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string","description":"The reminder id, as shown in reminder results."}}}`),
		Effect:      agent.EffectLocal,
		Label:       "Cancelling the reminder",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				ID agent.ID `json:"id"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ID == 0 {
				return agent.Result{}, httpx.BadRequest("id is required")
			}
			r, err := s.Cancel(ctx, int64(a.ID))
			if err != nil {
				return agent.Result{}, err
			}
			return s.result(ctx, "Reminder cancelled: "+oneLine(r), r)
		},
	}
}

// result builds a tool result whose card shows the upcoming reminders with r
// highlighted (added to the list when it is no longer upcoming).
func (s *Service) result(ctx context.Context, content string, r Reminder) (agent.Result, error) {
	items, err := s.List(ctx, ListUpcoming)
	if err != nil {
		return agent.Result{}, err
	}
	if len(items) > maxCardItems {
		items = items[:maxCardItems]
	}
	if !slices.ContainsFunc(items, func(x Reminder) bool { return x.ID == r.ID }) {
		items = append(items, r)
	}
	card, err := reminderCard("Reminders", items, []string{r.ID})
	return agent.Result{Content: content, Card: card}, err
}

func reminderCard(title string, items []Reminder, highlight []string) (*agent.Card, error) {
	if items == nil {
		items = []Reminder{}
	}
	b, err := json.Marshal(struct {
		Items     []Reminder `json:"items"`
		Highlight []string   `json:"highlight,omitempty"`
	}{items, highlight})
	if err != nil {
		return nil, fmt.Errorf("encode reminders card: %w", err)
	}
	return &agent.Card{Kind: "reminders", ID: "reminders", Title: title, Data: b}, nil
}

// oneLine describes a reminder for the model, id included.
func oneLine(r Reminder) string {
	s := fmt.Sprintf("[%s] %q at %s (%s", r.ID, r.Text, r.At, r.Status)
	if r.RRule != nil {
		s += ", repeats " + *r.RRule
	}
	return s + ")"
}
