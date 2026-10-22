package routines

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/mail"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/reminders"
	"github.com/loafykitten/hekate/service/internal/tasks"
	"github.com/loafykitten/hekate/service/internal/weather"
)

type fakeWeather struct{ err error }

func (f fakeWeather) Forecast(context.Context, string) (weather.Weather, error) {
	return weather.Weather{Place: "Home", Unit: "F", Now: weather.Now{Temp: 41, Condition: "Rain"}}, f.err
}

type fakeReminders struct{ list []reminders.Reminder }

func (f fakeReminders) List(_ context.Context, which string) ([]reminders.Reminder, error) {
	var out []reminders.Reminder
	for _, r := range f.list {
		past := r.Status != "" && r.Status != reminders.StatusScheduled
		if past == (which == reminders.ListPast) {
			out = append(out, r)
		}
	}
	return out, nil
}

func briefing(t *testing.T, f *fixture) (string, []string) {
	t.Helper()
	tool := f.svc.Tools()[1]
	if tool.Name != BriefingTool {
		t.Fatalf("tool = %q", tool.Name)
	}
	res, err := tool.Run(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, c := range res.Cards {
		kinds = append(kinds, c.Kind)
	}
	return res.Content, kinds
}

func TestBriefingGathersTheNextDay(t *testing.T) {
	f := newFixture(t) // 14:00: a late start
	at := func(d, h, m int) string { return time.Date(2026, 10, d, h, m, 0, 0, f.loc).Format(time.RFC3339) }
	f.svc.weather = fakeWeather{}
	f.svc.reminders = fakeReminders{list: []reminders.Reminder{
		{ID: "1", Text: "Take the bins out", At: at(20, 18, 0)},
		{ID: "2", Text: "Call the vet", At: at(21, 9, 0)},
		{ID: "3", Text: "Next week's thing", At: at(27, 9, 0)},
		{ID: "4", Text: "Water the plants", At: at(20, 6, 0), Status: reminders.StatusFired},
		{ID: "5", Text: "Ancient fired thing", At: at(10, 6, 0), Status: reminders.StatusFired},
		{ID: "6", Text: "Finished thing", At: at(20, 7, 0), Status: reminders.StatusDone},
	}}
	f.cal.events = []calendar.Event{
		{UID: "d", Title: "Dentist", Start: at(20, 15, 0), End: at(20, 16, 0), Location: "12 Main St"},
		{UID: "e", Title: "Early call", Start: at(21, 10, 0), End: at(21, 10, 30)},
		{UID: "m", Title: "Market", AllDay: true, Start: "2026-10-21", End: "2026-10-22"},
	}
	f.tasks.list = []tasks.Task{{ID: "1", Title: "Pay rent", Due: str("2026-10-19")}}
	gist := "Renewal terms"
	f.mail.attention = []mail.Message{{ID: "9", From: mail.Address{Name: "Landlord"}, Subject: "Lease", Gist: &gist}}
	f.followUps.due = []memory.Memory{{ID: "5", Content: "Ask how Mika's visit went"}}

	out, kinds := briefing(t, f)
	for _, want := range []string{"Give this as your morning briefing", "Today is Tuesday, October 20, 2026. It's 14:00 now.", "Rain",
		"15:00–16:00 Dentist at 12 Main St", "Wed 10:00–10:30 Early call", "All day Wed Oct 21: Market", "Pay rent (due 2026-10-19)", "18:00 Take the bins out",
		"Wed 09:00 Call the vet", "fired 06:00: Water the plants", "Landlord: Lease (Renewal terms)", "<<<MAIL ", "Ask how Mika's visit went"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"Ancient fired thing", "Finished thing"} {
		if strings.Contains(out, not) {
			t.Errorf("%q is in the briefing", not)
		}
	}
	if strings.Contains(out, "Next week's thing") {
		t.Error("a reminder a week out is in the briefing")
	}
	if strings.Join(kinds, ",") != "weather,mail,tasks" {
		t.Errorf("cards = %v", kinds)
	}
}

func TestBriefingSurvivesAFailingSource(t *testing.T) {
	f := newFixture(t)
	f.svc.weather = fakeWeather{err: errors.New("down")}
	f.cal.err = errors.New("calendar down")
	out, _ := briefing(t, f)
	if !strings.Contains(out, "(The weather couldn't be checked.)") || !strings.Contains(out, "(The calendar couldn't be checked.)") {
		t.Errorf("result:\n%s", out)
	}
}

func TestTheDaysFirstGoodMorningOpensWithTheBriefing(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	open := func(text string) func(bool) {
		op, ok := f.svc.Opener(ctx, text)
		if !ok {
			return nil
		}
		if op.Tool != BriefingTool || op.Settle == nil {
			t.Fatalf("opening = %+v", op)
		}
		return op.Settle
	}
	if open("morning! what's the weather?") != nil || open("good evening") != nil {
		t.Error("only \"good morning\" starts the briefing")
	}
	if open("when I come through later and say, oh, good morning, what would you do?") != nil {
		t.Error("a hypothetical good morning inside a long question started the briefing")
	}
	settle := open("hey Renamon, good morning!")
	if settle == nil {
		t.Fatal("the day's first good morning didn't start it")
	}
	if open("good morning") != nil {
		t.Error("a second good morning started another while the first was under way")
	}
	settle(false) // cut short or failed: the day isn't used up
	if settle = open("good morning, take two"); settle == nil {
		t.Fatal("a failed briefing can't be tried again")
	}
	settle(true)
	if open("good morning again") != nil {
		t.Error("a second good morning the same day started it again")
	}
	settle(false) // its playback was stopped partway after all
	if settle = open("good morning, from the top"); settle == nil {
		t.Fatal("a briefing stopped partway can't be tried again")
	}
	settle(true)
	f.now = time.Date(2026, 10, 21, 13, 50, 0, 0, f.loc) // the next day
	if open("good morning") == nil {
		t.Error("the next day's first good morning didn't start it")
	}
}
