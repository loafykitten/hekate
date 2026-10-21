package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/httpx"
)

const (
	maxSearchResults = 10
	maxSimilar       = 3
)

// Tools returns the model's memory tools.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{s.searchTool(), s.saveTool(), s.updateTool(), s.forgetTool(), s.profileSetTool(), s.profileRemoveTool()}
}

// ownerKeys are the profile lines only the owner sets, in the app: who they
// are. A name on a mail greeting or a guess must never change how she
// addresses them.
var ownerKeys = map[string]bool{
	"name": true, "first_name": true, "last_name": true, "middle_name": true, "full_name": true, "legal_name": true,
	"preferred_name": true, "chosen_name": true, "nickname": true, "surname": true, "pronouns": true, "gender": true,
}

const ownerKeyRefused = "Nothing was changed: only the owner sets their name, pronouns and gender, in the Memory section of the menu. " +
	"If they asked you to, tell them where. Never take these from mail or anywhere else."

const kindList = `"fact","preference","person","event","follow_up"`
const followUpHelp = "When it is worth asking about: 2026-10-24 (9:00 that day), 2026-10-24T15:00 (local time) or RFC 3339."

func (s *Service) searchTool() agent.Tool {
	return agent.Tool{
		Name: "memory_search",
		Description: "Search what you remember about the owner by words. Use it when a question might touch something they told you " +
			"before and it is not already in your context. Several short keywords work better than a sentence.",
		Schema: json.RawMessage(`{"type":"object","required":["query"],"properties":{
			"query":{"type":"string","description":"Keywords to look for."},
			"kind":{"type":"string","enum":[` + kindList + `],"description":"Only this kind."}
		}}`),
		Effect: agent.EffectRead,
		Label:  "Thinking back",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Query string `json:"query"`
				Kind  string `json:"kind"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if MatchQuery(a.Query) == "" {
				return agent.Result{}, httpx.BadRequest("query needs at least one keyword")
			}
			found, err := s.Search(ctx, a.Query, a.Kind, maxSearchResults)
			if err != nil {
				return agent.Result{}, err
			}
			if len(found) == 0 {
				return agent.Result{Content: "No matching memories."}, nil
			}
			card, err := memoryCard("What I remember", found)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: describe(found), Card: card}, nil
		},
	}
}

func (s *Service) saveTool() agent.Tool {
	return agent.Tool{
		Name: "memory_save",
		Description: "Remember something durable about the owner: a preference, a person in their life, an important date, a plan, a recurring struggle. " +
			"Do it quietly as it comes up; skip trivia. If the result lists similar memories, update one of those instead of keeping both. " +
			"Use follow_up_at for things worth asking about later (\"how did the interview go?\"). " +
			"Write content as one plain fact about the owner's life (\"Maya, their sister, is allergic to peanuts\"), with no " +
			"notes to yourself, importance or dates of saving in it; importance and follow_up_at are separate fields. " +
			"One fact per memory: an event on the calendar does not also need a memory.",
		Schema: json.RawMessage(`{"type":"object","required":["content","kind"],"properties":{
			"content":{"type":"string","description":"One self-contained note, e.g. \"Has a job interview at Acme on 2026-10-21\"."},
			"kind":{"type":"string","enum":[` + kindList + `]},
			"importance":{"type":"integer","minimum":1,"maximum":5,"description":"1 trivia, 3 normal (default), 5 must never forget (allergies, safety)."},
			"follow_up_at":{"type":"string","description":"` + followUpHelp + `"}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Making a note",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Content    string `json:"content"`
				Kind       string `json:"kind"`
				Importance int    `json:"importance"`
				FollowUpAt string `json:"follow_up_at"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			m, existed, err := s.Save(ctx, NewMemory{Kind: a.Kind, Content: a.Content, Importance: a.Importance,
				FollowUpAt: a.FollowUpAt, SourceTurn: agent.TurnID(ctx)})
			if err != nil {
				return agent.Result{}, err
			}
			content := "Saved: " + line(m, true)
			if existed {
				content = "Already remembered, nothing duplicated: " + line(m, true)
			} else if similar := s.similar(ctx, m); len(similar) > 0 {
				content += "\nSimilar memories you already have; if one says the same thing, memory_update it and memory_forget the new one:\n" + describe(similar)
			}
			card, err := memoryCard("Remembered", []Memory{m})
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: content, Card: card}, nil
		},
	}
}

// similar finds other memories that share words with m. A failure only costs
// the hint, so it is logged and ignored.
func (s *Service) similar(ctx context.Context, m Memory) []Memory {
	found, err := s.Search(ctx, m.Content, "", maxSimilar+1)
	if err != nil {
		s.log.Warn("cannot look for similar memories", "error", err)
		return nil
	}
	found = slices.DeleteFunc(found, func(x Memory) bool { return x.ID == m.ID })
	if len(found) > maxSimilar {
		found = found[:maxSimilar]
	}
	return found
}

func (s *Service) updateTool() agent.Tool {
	return agent.Tool{
		Name: "memory_update",
		Description: "Change a memory you already have: correct its content, kind or importance, or set follow_up_at (empty string clears it, " +
			"which you should do once you have followed up). Prefer this over saving a near-duplicate.",
		Schema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id":{"type":"string","description":"The memory id, as shown in memory results."},
			"content":{"type":"string"},
			"kind":{"type":"string","enum":[` + kindList + `]},
			"importance":{"type":"integer","minimum":1,"maximum":5},
			"follow_up_at":{"type":"string","description":"` + followUpHelp + ` Empty string clears it."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Updating a note",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				ID         agent.ID `json:"id"`
				Content    *string  `json:"content"`
				Kind       *string  `json:"kind"`
				Importance *int     `json:"importance"`
				FollowUpAt *string  `json:"follow_up_at"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ID == 0 {
				return agent.Result{}, httpx.BadRequest("id is required")
			}
			m, err := s.Update(ctx, int64(a.ID), Patch{Content: a.Content, Kind: a.Kind, Importance: a.Importance, FollowUpAt: a.FollowUpAt})
			if err != nil {
				return agent.Result{}, err
			}
			card, err := memoryCard("Remembered", []Memory{m})
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: "Updated: " + line(m, true), Card: card}, nil
		},
	}
}

func (s *Service) forgetTool() agent.Tool {
	return agent.Tool{
		Name: "memory_forget",
		Description: "Forget a memory for good. Do it right away when the owner asks you to forget something; find its id with memory_search if you do not have it. " +
			"It also works on memories your reflection retired (their ids are in diary_read). What you forget, your nightly reflection won't save again.",
		Schema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string","description":"The memory id, as shown in memory results."}}}`),
		Effect: agent.EffectLocal,
		Label:  "Forgetting it",
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
			m, err := s.Forget(ctx, int64(a.ID))
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: "Forgotten for good: " + line(m, false)}, nil
		},
	}
}

func (s *Service) profileSetTool() agent.Tool {
	return agent.Tool{
		Name: "profile_set",
		Description: "Set one line of the owner's profile, which you always have in view: stable facts that shape many answers " +
			"(diet, allergies, household, work, routines). Short values only; use memory_save for anything with detail or a date. Setting an existing key replaces it. " +
			"Not their name, pronouns or gender: the owner sets those in the app.",
		Schema: json.RawMessage(`{"type":"object","required":["key","value"],"properties":{
			"key":{"type":"string","description":"Short label such as diet, pronouns, partner, job."},
			"value":{"type":"string","description":"A short value, e.g. \"vegetarian\"."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Updating what I know",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if ownerKeys[NormalizeKey(a.Key)] {
				return agent.Result{Content: ownerKeyRefused}, nil
			}
			e, err := s.SetProfile(ctx, a.Key, a.Value)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: fmt.Sprintf("Profile saved: %s = %s", e.Key, e.Value)}, nil
		},
	}
}

func (s *Service) profileRemoveTool() agent.Tool {
	return agent.Tool{
		Name:        "profile_remove",
		Description: "Remove one line from the owner's profile, e.g. when it is no longer true or they ask you to forget it.",
		Schema:      json.RawMessage(`{"type":"object","required":["key"],"properties":{"key":{"type":"string","description":"The profile key, as shown in your context."}}}`),
		Effect:      agent.EffectLocal,
		Label:       "Updating what I know",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Key string `json:"key"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if ownerKeys[NormalizeKey(a.Key)] {
				return agent.Result{Content: ownerKeyRefused}, nil
			}
			if err := s.RemoveProfile(ctx, a.Key); err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: "Removed from the profile: " + NormalizeKey(a.Key)}, nil
		},
	}
}

func memoryCard(title string, items []Memory) (*agent.Card, error) {
	b, err := json.Marshal(struct {
		Items []Memory `json:"items"`
	}{items})
	if err != nil {
		return nil, fmt.Errorf("encode memory card: %w", err)
	}
	return &agent.Card{Kind: "memory", ID: "memory", Title: title, Data: b}, nil
}

func describe(ms []Memory) string {
	lines := make([]string, len(ms))
	for i, m := range ms {
		lines[i] = fmt.Sprintf("- %s (importance %d)", line(m, true), m.Importance)
	}
	return strings.Join(lines, "\n")
}
