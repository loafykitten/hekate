package reflection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Tools returns diary_read.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{{
		Name: "diary_read",
		Description: "Read your own diary: the entry you wrote reflecting on a day, and what that night's reflection " +
			"changed in your memories. Without a day, your latest entry. Share it only if the owner asks; it's yours.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"day":{"type":"string","description":"YYYY-MM-DD; omit for the latest entry."}
		}}`),
		Effect: agent.EffectRead,
		Label:  "Reading my diary",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Day string `json:"day"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			out, err := s.readDiary(ctx, strings.TrimSpace(a.Day))
			return agent.Result{Content: out}, err
		},
	}}
}

func (s *Service) readDiary(ctx context.Context, day string) (string, error) {
	var e *DiaryEntry
	var err error
	if day == "" {
		e, err = s.latestDiary(ctx, "")
	} else {
		if _, perr := time.Parse(time.DateOnly, day); perr != nil {
			return "", httpx.BadRequest("day must be YYYY-MM-DD")
		}
		e, err = s.diary(ctx, day)
	}
	if err != nil {
		return "", err
	}
	if e == nil {
		if day == "" {
			return "You haven't written a diary entry yet: the first comes after your first nightly reflection.", nil
		}
		return fmt.Sprintf("You wrote no diary entry for %s.", day), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your diary, %s:\n%s\n", e.Day, e.Text)
	refl, err := s.reflections(ctx, e.Day)
	if err != nil {
		return "", err
	}
	for _, r := range refl {
		fmt.Fprintf(&b, "\nThat night's reflection: %s\n", r.Summary)
		for _, c := range r.Changes {
			b.WriteString("- " + describeChange(c) + "\n")
		}
	}
	return b.String(), nil
}

// describeChange says what a change did, in a line.
func describeChange(c Change) string {
	var out string
	switch {
	case c.Action == ActionSave && c.After != nil:
		out = fmt.Sprintf("saved memory %s, %q", c.MemoryID, c.After.Content)
	case c.Action == ActionUpdate && c.After != nil:
		out = fmt.Sprintf("updated memory %s to %q", c.MemoryID, c.After.Content)
	case c.Action == ActionRetire && c.Before != nil:
		out = fmt.Sprintf("retired memory %s, %q", c.MemoryID, c.Before.Content)
	default:
		out = c.Action + " memory " + c.MemoryID
	}
	if c.Reason != "" {
		out += " (" + c.Reason + ")"
	}
	if c.UndoneAt != nil {
		out += "; the owner undid this"
	}
	return out
}
