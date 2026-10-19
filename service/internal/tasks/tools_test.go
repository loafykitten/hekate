package tasks

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

func tool(t *testing.T, s *Service, name string) agent.Tool {
	t.Helper()
	for _, tl := range s.Tools() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %q", name)
	return agent.Tool{}
}

func run(t *testing.T, s *Service, name, args string) (agent.Result, error) {
	t.Helper()
	return tool(t, s, name).Run(context.Background(), json.RawMessage(args))
}

type cardData struct {
	Items     []Task   `json:"items"`
	Highlight []string `json:"highlight"`
}

func cardOf(t *testing.T, r agent.Result) cardData {
	t.Helper()
	if r.Card == nil || r.Card.Kind != "tasks" {
		t.Fatalf("card = %+v", r.Card)
	}
	var d cardData
	if err := json.Unmarshal(r.Card.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestToolsRegisterCleanly(t *testing.T) {
	f := newFixture(t)
	if _, err := agent.NewRegistry(f.svc.Tools()...); err != nil {
		t.Fatal(err)
	}
	effects := map[string]agent.Effect{}
	for _, tl := range f.svc.Tools() {
		effects[tl.Name] = tl.Effect
	}
	for name, want := range map[string]agent.Effect{
		"task_list": agent.EffectRead, "task_create": agent.EffectLocal, "task_update": agent.EffectLocal,
		"task_complete": agent.EffectLocal, "task_delete": agent.EffectLocal,
	} {
		if effects[name] != want {
			t.Errorf("%s effect = %q, want %q", name, effects[name], want)
		}
	}
}

func TestToolCreateSeveralShowsTheWholeListHighlighted(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f.svc, NewTask{Title: "Bread", List: "Groceries"})
	before := f.events.count()

	res, err := run(t, f.svc, "task_create", `{"titles":["Oat milk","Eggs"],"list":"groceries"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "Added 2 tasks to Groceries") || !strings.Contains(res.Content, `"Oat milk"`) || !strings.Contains(res.Content, "[2]") {
		t.Errorf("content = %q", res.Content)
	}
	d := cardOf(t, res)
	if len(d.Items) != 3 || len(d.Highlight) != 2 || d.Highlight[0] != "2" || d.Highlight[1] != "3" {
		t.Errorf("card = %+v", d)
	}
	if res.Card.Title != "Groceries" {
		t.Errorf("card title = %q", res.Card.Title)
	}
	if f.events.count() != before+1 {
		t.Errorf("a batch should invalidate once; got %d", f.events.count()-before)
	}
}

func TestToolCreateRepeatingAndErrors(t *testing.T) {
	f := newFixture(t)
	res, err := run(t, f.svc, "task_create", `{"title":"Take out the recycling","rrule":"FREQ=WEEKLY;BYDAY=MO"}`)
	if err != nil || !strings.Contains(res.Content, "due 2026-10-19") || !strings.Contains(res.Content, "repeats FREQ=WEEKLY;BYDAY=MO") {
		t.Fatalf("%v %q", err, res.Content)
	}
	for _, args := range []string{`{}`, `{"title":"x","due":"tomorrow"}`, `{"title":"x","nope":1}`, `{"title":"x","priority":7}`} {
		if _, err := run(t, f.svc, "task_create", args); err == nil {
			t.Errorf("task_create %s accepted", args)
		}
	}
	// A failure part-way reports what was already added.
	_, err = run(t, f.svc, "task_create", `{"titles":["ok one","  "]}`)
	if err == nil || !strings.Contains(err.Error(), "already added") || !strings.Contains(err.Error(), "ok one") {
		t.Errorf("partial failure error = %v", err)
	}
}

func TestToolListGroupsOpenTasks(t *testing.T) {
	f := newFixture(t)
	mustCreate(t, f.svc, NewTask{Title: "Oat milk", List: "Groceries"})
	mustCreate(t, f.svc, NewTask{Title: "Eggs", List: "Groceries", Due: "2026-10-20"})
	mustCreate(t, f.svc, NewTask{Title: "Call dentist"})
	done := mustCreate(t, f.svc, NewTask{Title: "Old", List: "Groceries"})
	if _, err := f.svc.Complete(t.Context(), mustID(t, done)); err != nil {
		t.Fatal(err)
	}

	res, err := run(t, f.svc, "task_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "Groceries (2):\n- [2] Eggs (due 2026-10-20)\n- [1] Oat milk\n\nInbox (1):\n- [3] Call dentist"
	if res.Content != want {
		t.Errorf("content =\n%s\nwant\n%s", res.Content, want)
	}
	if d := cardOf(t, res); len(d.Items) != 3 || d.Highlight != nil {
		t.Errorf("card = %+v", d)
	}

	res, _ = run(t, f.svc, "task_list", `{"list":"groceries","status":"done"}`)
	if !strings.Contains(res.Content, "Old") || strings.Contains(res.Content, "Oat milk") {
		t.Errorf("done list = %q", res.Content)
	}
	res, _ = run(t, f.svc, "task_list", `{"due_before":"2026-10-21"}`)
	if !strings.Contains(res.Content, "Eggs") || strings.Contains(res.Content, "Oat milk") {
		t.Errorf("due_before = %q", res.Content)
	}
	res, _ = run(t, f.svc, "task_list", `{"list":"Nothing"}`)
	if res.Content != "No tasks match." {
		t.Errorf("empty = %q", res.Content)
	}
	if _, err := run(t, f.svc, "task_list", `{"due_before":"soon"}`); err == nil {
		t.Error("bad due_before accepted")
	}
}

func TestToolUpdateCompleteDelete(t *testing.T) {
	f := newFixture(t)
	a := mustCreate(t, f.svc, NewTask{Title: "Eggs", List: "Groceries"})
	rep := mustCreate(t, f.svc, NewTask{Title: "Recycling", RRule: "FREQ=WEEKLY;BYDAY=MO"})

	// Ids may be strings or numbers.
	res, err := run(t, f.svc, "task_update", `{"id":1,"title":"Free-range eggs","due":"2026-10-22T09:00"}`)
	if err != nil || !strings.Contains(res.Content, `[1] "Free-range eggs"`) || !strings.Contains(res.Content, "due 2026-10-22T09:00:00-05:00") {
		t.Fatalf("update: %v %q", err, res.Content)
	}
	if d := cardOf(t, res); len(d.Highlight) != 1 || d.Highlight[0] != "1" {
		t.Errorf("card = %+v", d)
	}

	res, err = run(t, f.svc, "task_complete", `{"id":"1"}`)
	if err != nil || !strings.HasPrefix(res.Content, "Completed:") {
		t.Fatalf("complete: %v %q", err, res.Content)
	}
	// The finished task is still on the card, highlighted, though no longer open.
	d := cardOf(t, res)
	if len(d.Items) != 1 || d.Items[0].CompletedAt == nil || d.Highlight[0] != a.ID {
		t.Errorf("card = %+v", d)
	}
	res, _ = run(t, f.svc, "task_complete", `{"id":1}`)
	if !strings.HasPrefix(res.Content, "It was already done") {
		t.Errorf("again = %q", res.Content)
	}

	res, err = run(t, f.svc, "task_complete", `{"id":`+rep.ID+`}`)
	if err != nil || !strings.Contains(res.Content, "it repeats, so it is now due 2026-10-26") {
		t.Fatalf("complete repeating: %v %q", err, res.Content)
	}

	res, err = run(t, f.svc, "task_delete", `{"id":2}`)
	if err != nil || !strings.HasPrefix(res.Content, "Deleted:") {
		t.Fatalf("delete: %v %q", err, res.Content)
	}
	if d := cardOf(t, res); len(d.Highlight) != 0 {
		t.Errorf("a deleted task cannot be highlighted: %+v", d)
	}

	for _, c := range []struct{ tool, args string }{
		{"task_update", `{}`}, {"task_update", `{"id":99,"title":"x"}`}, {"task_update", `{"id":"abc"}`},
		{"task_complete", `{"id":99}`}, {"task_delete", `{"id":99}`}, {"task_delete", `{}`},
	} {
		if _, err := run(t, f.svc, c.tool, c.args); err == nil {
			t.Errorf("%s %s accepted", c.tool, c.args)
		}
	}
}

// The occurrence she saw was completed elsewhere before the tool ran: the
// tool must not complete the next one too.
func TestCompleteToolUsesTheSeenOccurrence(t *testing.T) {
	f := newFixture(t)
	f.setNow(time.Date(2026, 10, 19, 8, 0, 0, 0, f.svc.loc))
	task := mustCreate(t, f.svc, NewTask{Title: "Recycling", Due: "2026-10-19", RRule: "FREQ=DAILY"})
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	seen := *task.Due
	if _, err := f.svc.CompleteOccurrence(t.Context(), id, &seen); err != nil { // the menu, first
		t.Fatal(err)
	}
	var tool agent.Tool
	for _, tl := range f.svc.Tools() {
		if tl.Name == "task_complete" {
			tool = tl
		}
	}
	res, err := tool.Run(t.Context(), json.RawMessage(`{"id":"`+task.ID+`","due":"`+seen+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(t.Context(), id)
	if got.Due == nil || *got.Due != "2026-10-20" || !strings.Contains(res.Content, "already checked off") {
		t.Errorf("due = %v, content = %q; want 2026-10-20 and a note that it was already done", got.Due, res.Content)
	}
}
