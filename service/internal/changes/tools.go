package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/approvals"
)

// Tools returns the model's change-request tool.
func (s *Service) Tools() []agent.Tool { return []agent.Tool{s.proposeTool()} }

func (s *Service) proposeTool() agent.Tool {
	return agent.Tool{
		Name: "propose_change",
		Description: "Propose a change to Hekate itself: a feature you want, a bug you hit, or something that would work better. " +
			"The owner approves or denies it; once approved, it goes on their list to build with Claude Code, and you see " +
			"how it's going here. Changes are built in a public repository, so propose only what is relevant to how " +
			"Hekate works, written for a stranger reading a public issue: no personal context, nothing remotely NSFW, and " +
			"nothing that identifies the owner beyond their name and pronouns (no addresses, accounts, senders, places, " +
			"health, money or schedules; say \"a bank's marketing mail\", not which bank). " +
			"Describe the behavior wanted, not code. At most 5 proposals in 24 hours. " +
			"If the owner denied one with a reason, you may propose a revised version that addresses it, naming it in revises.",
		Schema: json.RawMessage(`{"type":"object","required":["title","what","why","kind"],"properties":{
			"title":{"type":"string","description":"Short and specific, under 80 characters, e.g. \"Match mail rules on subject words\"."},
			"what":{"type":"string","description":"What to build: the behavior you want, concretely, in a few sentences or a short list."},
			"why":{"type":"string","description":"Why it helps, in a sentence or two."},
			"kind":{"type":"string","enum":["feature","fix","improvement"]},
			"revises":{"type":"integer","description":"The id of the denied proposal this replaces."}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Writing up a change",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var p Proposal
			if err := agent.DecodeArgs(raw, &p); err != nil {
				return agent.Result{}, err
			}
			c, a, err := s.Propose(ctx, p)
			if errors.Is(err, ErrCap) {
				return agent.Result{Content: ErrCap.Error() + ". Keep the idea and bring it up later."}, nil
			}
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{
				Content: fmt.Sprintf("Proposed as change %s. The owner decides on the card; if they allow it, it goes on their list to build.", c.ID),
				Card:    approvalCard(a),
			}, nil
		},
	}
}

// approvalCard shows the proposal's approval, as the other approval tools do.
func approvalCard(a approvals.Approval) *agent.Card {
	data, err := json.Marshal(a)
	if err != nil {
		return nil
	}
	return &agent.Card{Kind: "approval", ID: a.ID, Title: a.Title, Data: data}
}
