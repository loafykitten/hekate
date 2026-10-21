package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/when"
)

// maxCardItems keeps a card readable on a phone.
const (
	maxCardItems = 25
	maxToolRows  = 100
	maxBatch     = 20
)

// Tools returns the model's task tools.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{s.listTool(), s.createTool(), s.updateTool(), s.completeTool(), s.deleteTool()}
}

const dueHelp = "Local time unless it has an offset: 2026-10-20 (all day), 2026-10-20T09:00, or RFC 3339."

func (s *Service) listTool() agent.Tool {
	return agent.Tool{
		Name: "task_list",
		Description: "Look up tasks and lists. With no arguments it returns every open task grouped by list. " +
			"Use it before answering anything about what is on a list or what is due.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"list":{"type":"string","description":"Only this list, e.g. Groceries. Case does not matter."},
			"status":{"type":"string","enum":["open","done","all"],"description":"Default open."},
			"due_before":{"type":"string","description":"Only tasks due before this moment (exclusive). A date means before that day starts, so use tomorrow's date to get everything due today or earlier. ` + dueHelp + `"}
		}}`),
		Effect: agent.EffectRead,
		Label:  "Checking your tasks",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				List      string `json:"list"`
				Status    string `json:"status"`
				DueBefore string `json:"due_before"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			f := Filter{List: a.List, Status: Status(a.Status), Limit: maxToolRows}
			if a.DueBefore != "" {
				v, err := when.Parse(a.DueBefore, s.loc)
				if err != nil {
					return agent.Result{}, httpx.BadRequest("due_before: %s", err)
				}
				f.DueBefore = &v
			}
			tasks, err := s.List(ctx, f)
			if err != nil {
				return agent.Result{}, err
			}
			title := "Tasks"
			if a.List != "" {
				title = a.List
				if len(tasks) > 0 {
					title = tasks[0].List
				}
			}
			// Grouping is for the model's benefit; the card keeps the same order.
			slices.SortStableFunc(tasks, func(x, y Task) int { return strings.Compare(strings.ToLower(x.List), strings.ToLower(y.List)) })
			card, err := Card(title, tasks, nil)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: describeList(tasks), Card: card}, nil
		},
	}
}

type createArgs struct {
	Title    string   `json:"title"`
	Titles   []string `json:"titles"`
	Notes    string   `json:"notes"`
	List     string   `json:"list"`
	Due      string   `json:"due"`
	RRule    string   `json:"rrule"`
	Priority int      `json:"priority"`
}

func (s *Service) createTool() agent.Tool {
	return agent.Tool{
		Name: "task_create",
		Description: "Add a task, or several tasks that share a list and due date, e.g. grocery items. " +
			"Give `title` for one task or `titles` for several. For something that repeats, set `rrule` " +
			"(\"every Monday\" is FREQ=WEEKLY;BYDAY=MO) and leave `due` out to start from the next occurrence.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string","description":"One task."},
			"titles":{"type":"array","items":{"type":"string"},"description":"Several tasks added together."},
			"list":{"type":"string","description":"List name such as Groceries. Defaults to Inbox. Reuse an existing list's name."},
			"due":{"type":"string","description":"` + dueHelp + `"},
			"rrule":{"type":"string","description":"RFC 5545 RRULE for repeating tasks, e.g. FREQ=WEEKLY;BYDAY=MO or FREQ=MONTHLY;BYMONTHDAY=1."},
			"priority":{"type":"integer","minimum":0,"maximum":3,"description":"0 none, 1 low, 2 medium, 3 high."},
			"notes":{"type":"string"}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Adding to your list",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a createArgs
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			titles := a.Titles
			if strings.TrimSpace(a.Title) != "" {
				titles = append([]string{a.Title}, titles...)
			}
			if len(titles) == 0 {
				return agent.Result{}, httpx.BadRequest("give a title (or titles)")
			}
			if len(titles) > maxBatch {
				return agent.Result{}, httpx.BadRequest("at most %d tasks at a time", maxBatch)
			}
			var created []Task
			for _, title := range titles {
				t, err := s.create(ctx, NewTask{Title: title, Notes: a.Notes, List: a.List, Due: a.Due, RRule: a.RRule, Priority: a.Priority})
				if err != nil {
					return partial(created, err)
				}
				created = append(created, t)
			}
			s.emitChanged()
			card, err := s.changedCard(ctx, created[0].List, created, true)
			if err != nil {
				return agent.Result{}, err
			}
			var lines []string
			for _, t := range created {
				lines = append(lines, "- "+oneLine(t))
			}
			return agent.Result{
				Content: fmt.Sprintf("Added %d %s to %s:\n%s", len(created), plural(len(created), "task", "tasks"), created[0].List, strings.Join(lines, "\n")),
				Card:    card,
			}, nil
		},
	}
}

// partial reports tasks created before a failure, so the model does not retry them.
func partial(created []Task, err error) (agent.Result, error) {
	if len(created) == 0 {
		return agent.Result{}, err
	}
	var lines []string
	for _, t := range created {
		lines = append(lines, oneLine(t))
	}
	return agent.Result{}, fmt.Errorf("%w (already added before this failed: %s)", err, strings.Join(lines, "; "))
}

type updateArgs struct {
	ID       agent.ID `json:"id"`
	Title    *string  `json:"title"`
	Notes    *string  `json:"notes"`
	List     *string  `json:"list"`
	Due      *string  `json:"due"`
	RRule    *string  `json:"rrule"`
	Priority *int     `json:"priority"`
}

func (s *Service) updateTool() agent.Tool {
	return agent.Tool{
		Name:        "task_update",
		Description: "Change a task: rename it, move it to another list, set or clear its due date or repeat rule, change priority. Pass only what changes; an empty string clears `due` or `rrule`.",
		Schema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id":{"type":"string","description":"The task id, as shown in task results."},
			"title":{"type":"string"},"notes":{"type":"string"},"list":{"type":"string"},
			"due":{"type":"string","description":"` + dueHelp + ` Empty string clears it."},
			"rrule":{"type":"string","description":"RFC 5545 RRULE. Empty string stops repeating."},
			"priority":{"type":"integer","minimum":0,"maximum":3}
		}}`),
		Effect: agent.EffectLocal,
		Label:  "Updating your task",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a updateArgs
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ID == 0 {
				return agent.Result{}, httpx.BadRequest("id is required")
			}
			t, err := s.Update(ctx, int64(a.ID), Patch{Title: a.Title, Notes: a.Notes, List: a.List, Due: a.Due, RRule: a.RRule, Priority: a.Priority})
			if err != nil {
				return agent.Result{}, err
			}
			card, err := s.changedCard(ctx, t.List, []Task{t}, true)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: "Updated: " + oneLine(t), Card: card}, nil
		},
	}
}

func (s *Service) completeTool() agent.Tool {
	return agent.Tool{
		Name: "task_complete",
		Description: "Mark a task done. A repeating task is not closed: it moves to its next occurrence, and the result says when. " +
			"Pass the due date shown with the task, so the occurrence you mean is the one completed even if it changed meanwhile.",
		Schema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id":{"type":"string","description":"The task id, as shown in task results."},
			"due":{"type":"string","description":"The task's due exactly as shown in task results (omit for a task without one)."}}}`),
		Effect: agent.EffectLocal,
		Label:  "Checking it off",
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				ID  agent.ID `json:"id"`
				Due *string  `json:"due"`
			}
			if err := agent.DecodeArgs(raw, &a); err != nil {
				return agent.Result{}, err
			}
			if a.ID == 0 {
				return agent.Result{}, httpx.BadRequest("id is required")
			}
			before, err := s.Get(ctx, int64(a.ID))
			if err != nil {
				return agent.Result{}, err
			}
			// Complete the occurrence she saw (the due from the task results); if
			// it was completed meanwhile (the menu, another device), don't advance
			// the next one too. Without it, the current one.
			due := ""
			if before.Due != nil {
				due = *before.Due
			}
			if a.Due != nil {
				due = *a.Due
			}
			t, err := s.CompleteOccurrence(ctx, int64(a.ID), &due)
			if err != nil {
				return agent.Result{}, err
			}
			content := "Completed: " + oneLine(t)
			switch {
			case before.CompletedAt != nil:
				content = "It was already done: " + oneLine(t)
			case a.Due != nil && !sameDue(before.Due, *a.Due):
				content = "That occurrence was already checked off (maybe on another device); nothing changed: " + oneLine(t)
			case t.RRule != nil && t.CompletedAt == nil:
				content = fmt.Sprintf("Completed this time; it repeats, so it is now due %s: %s", deref(t.Due), oneLine(t))
			case t.RRule != nil:
				content = "Completed, and its repeat rule has ended: " + oneLine(t)
			}
			card, err := s.changedCard(ctx, t.List, []Task{t}, true)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: content, Card: card}, nil
		},
	}
}

func (s *Service) deleteTool() agent.Tool {
	return agent.Tool{
		Name:        "task_delete",
		Description: "Delete a task for good (it disappears from every list). Use it when the owner asks to remove or cancel a task, not to finish one.",
		Schema:      json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string","description":"The task id, as shown in task results."}}}`),
		Effect:      agent.EffectLocal,
		Label:       "Removing the task",
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
			t, err := s.Delete(ctx, int64(a.ID))
			if err != nil {
				return agent.Result{}, err
			}
			// The deleted task cannot be highlighted: the card shows what is left.
			card, err := s.changedCard(ctx, t.List, nil, false)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Content: "Deleted: " + oneLine(t), Card: card}, nil
		},
	}
}

// emitChanged tells clients tasks changed, once for a batch.
func (s *Service) emitChanged() { s.events.Invalidate(proto.DomainTasks) }

// changedCard shows the task's list as it stands now, with the changed tasks
// highlighted. A changed task that is not in the open list (just completed) is
// added so the owner sees it.
func (s *Service) changedCard(ctx context.Context, list string, changed []Task, highlight bool) (*agent.Card, error) {
	open, err := s.List(ctx, Filter{List: list, Limit: maxCardItems})
	if err != nil {
		return nil, err
	}
	items := open
	var ids []string
	for _, c := range changed {
		if highlight {
			ids = append(ids, c.ID)
		}
		if !slices.ContainsFunc(items, func(t Task) bool { return t.ID == c.ID }) {
			items = append(items, c)
		}
	}
	return Card(list, items, ids)
}

// Card is a `tasks` card titled title, highlighting the given ids.
func Card(title string, items []Task, highlight []string) (*agent.Card, error) {
	return card(title, items, highlight, false)
}

// RecordCard is a `tasks` card of what was done, shown without controls: an
// item may be one completion of a repeating task, which the series' controls
// can't undo.
func RecordCard(title string, items []Task) (*agent.Card, error) {
	return card(title, items, nil, true)
}

func card(title string, items []Task, highlight []string, record bool) (*agent.Card, error) {
	if len(items) > maxCardItems {
		items = items[:maxCardItems]
	}
	if items == nil {
		items = []Task{}
	}
	data := struct {
		Items     []Task   `json:"items"`
		Highlight []string `json:"highlight,omitempty"`
		Record    bool     `json:"record,omitempty"`
	}{items, highlight, record}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode tasks card: %w", err)
	}
	return &agent.Card{Kind: "tasks", ID: "tasks:" + strings.ToLower(title), Title: title, Data: b}, nil
}

// oneLine describes a task for the model, ids included so it can refer to it.
func oneLine(t Task) string {
	parts := []string{"list " + t.List}
	if t.Due != nil {
		parts = append(parts, "due "+*t.Due)
	}
	if t.RRule != nil {
		parts = append(parts, "repeats "+*t.RRule)
	}
	if t.Priority > 0 {
		parts = append(parts, fmt.Sprintf("priority %d", t.Priority))
	}
	if t.CompletedAt != nil {
		parts = append(parts, "done")
	}
	return fmt.Sprintf("[%s] %q (%s)", t.ID, t.Title, strings.Join(parts, ", "))
}

// describeList renders tasks (already ordered by list) for the model.
func describeList(tasks []Task) string {
	if len(tasks) == 0 {
		return "No tasks match."
	}
	var (
		b    strings.Builder
		last string
	)
	for i, t := range tasks {
		if i == 0 || !strings.EqualFold(t.List, last) {
			if i > 0 {
				b.WriteString("\n")
			}
			n := 0
			for _, u := range tasks[i:] {
				if strings.EqualFold(u.List, t.List) {
					n++
				}
			}
			fmt.Fprintf(&b, "%s (%d):\n", t.List, n)
			last = t.List
		}
		b.WriteString("- " + taskLine(t) + "\n")
	}
	if len(tasks) >= maxToolRows {
		b.WriteString("(showing the first " + fmt.Sprint(maxToolRows) + ")\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func taskLine(t Task) string {
	s := fmt.Sprintf("[%s] %s", t.ID, t.Title)
	var extra []string
	if t.Due != nil {
		extra = append(extra, "due "+*t.Due)
	}
	if t.RRule != nil {
		extra = append(extra, "repeats "+*t.RRule)
	}
	if t.Priority > 0 {
		extra = append(extra, fmt.Sprintf("priority %d", t.Priority))
	}
	if t.CompletedAt != nil {
		extra = append(extra, "done")
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
