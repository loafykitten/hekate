package routines

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/llm"
	"github.com/loafykitten/hekate/service/internal/mail"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/reminders"
	"github.com/loafykitten/hekate/service/internal/tasks"
	"github.com/loafykitten/hekate/service/internal/weather"
)

type fakeModel struct {
	mu    sync.Mutex
	reply string
	asked []llm.Request
}

func (m *fakeModel) Configured() bool { return true }
func (m *fakeModel) Complete(_ context.Context, req llm.Request) (string, *llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, req)
	return m.reply, &llm.Result{Model: "flex-1"}, nil
}

type fakeWeather struct{ err error }

func (f fakeWeather) Forecast(context.Context, string) (weather.Weather, error) {
	return weather.Weather{Place: "Home", Unit: "F", Now: weather.Now{Temp: 41, Condition: "Rain"}}, f.err
}

type fakeReminders struct{ list []reminders.Reminder }

func (f fakeReminders) List(context.Context, string) ([]reminders.Reminder, error) {
	return f.list, nil
}

type performed struct {
	origin, id string
	script     agent.Script
	quiet      bool
}

type fakePerformer struct{ got []performed }

func (p *fakePerformer) Perform(origin, id string, s agent.Script, quiet bool) {
	p.got = append(p.got, performed{origin, id, s, quiet})
}

func TestBriefingIsWrittenPushedAndPlayed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.now = time.Date(2026, 10, 20, 7, 30, 0, 0, f.loc)
	at := func(h, m int) string { return time.Date(2026, 10, 20, h, m, 0, 0, f.loc).Format(time.RFC3339) }
	model := &fakeModel{reply: "{waving} Morning, Rika. [softly] Rain all day, so take the umbrella to the dentist at three."}
	perf := &fakePerformer{}
	f.svc.llm, f.svc.model, f.svc.tamer = model, config.Model{Name: "flex"}, "Rika"
	f.svc.weather = fakeWeather{}
	f.svc.reminders = fakeReminders{list: []reminders.Reminder{
		{ID: "1", Text: "Take the bins out", At: at(18, 0)},
		{ID: "2", Text: "Tomorrow's thing", At: time.Date(2026, 10, 21, 9, 0, 0, 0, f.loc).Format(time.RFC3339)},
	}}
	f.svc.gestures = func() []string { return []string{"waving", "nodding"} }
	f.svc.SetPerformer(perf)
	f.cal.events = []calendar.Event{{UID: "d", Title: "Dentist", Start: at(15, 0), End: at(16, 0), Location: "12 Main St"}}
	f.tasks.list = []tasks.Task{{ID: "1", Title: "Pay rent", Due: str("2026-10-19")}}
	gist := "Renewal terms"
	f.mail.attention = []mail.Message{{ID: "9", From: mail.Address{Name: "Landlord"}, Subject: "Lease", Gist: &gist}}
	f.followUps.due = []memory.Memory{{ID: "5", Content: "Ask how Mika's visit went"}}

	if f.svc.BriefingWaiting(ctx) {
		t.Error("waiting before it was written")
	}
	summary, err := f.svc.WriteBriefing(ctx)
	if err != nil || summary != "Wrote the briefing for 2026-10-20 with 3 card(s)" {
		t.Fatalf("write: %q %v", summary, err)
	}
	req := model.asked[0]
	if sys := req.Messages[0].Content; !strings.Contains(sys, "You are Renamon") || !strings.Contains(sys, "from: waving, nodding") || req.Model != "flex" {
		t.Errorf("system prompt: %q", sys[len(sys)-400:])
	}
	in := req.Messages[1].Content
	for _, want := range []string{"Today is Tuesday, October 20, 2026", "Rain", "15:00–16:00 Dentist at 12 Main St", "Pay rent (due 2026-10-19)",
		"18:00 Take the bins out", "Landlord: Lease (Renewal terms)", "<<<MAIL ", "Ask how Mika's visit went"} {
		if !strings.Contains(in, want) {
			t.Errorf("input lacks %q:\n%s", want, in)
		}
	}
	if strings.Contains(in, "Tomorrow's thing") {
		t.Error("tomorrow's reminder is in today's briefing")
	}
	n := f.rec.notices[0]
	if n.Kind != proto.NotifyKindBriefing || n.Title != "Good morning" || n.Body != "Morning, Rika. Rain all day, so take the umbrella to the dentist at three." {
		t.Errorf("notice = %+v", n)
	}
	if p := f.rec.pushes[0]; p.URL != "/?menu=today" || p.Tag != "briefing" {
		t.Errorf("push = %+v", p)
	}
	if !f.svc.BriefingWaiting(ctx) {
		t.Error("not waiting after it was written")
	}

	if err := f.svc.PlayBriefing(ctx, "c1", "b1", true, false); err != nil {
		t.Fatal(err)
	}
	got := perf.got[0]
	kinds := []string{}
	for _, c := range got.script.Cards {
		kinds = append(kinds, c.Kind)
	}
	if got.origin != "c1" || got.id != "b1" || !got.quiet || got.script.Text != model.reply || got.script.Model != "flex-1" ||
		strings.Join(kinds, ",") != "weather,mail,tasks" || !strings.Contains(got.script.Cue, "gave Rika the morning briefing") {
		t.Errorf("performed = %+v (cards %v)", got, kinds)
	}
	if f.svc.BriefingWaiting(ctx) {
		t.Error("still waiting after it was played")
	}
	// A second device opening on its own doesn't take it from the first...
	if err := f.svc.PlayBriefing(ctx, "c2", "b2", false, false); !errors.Is(err, ErrNoBriefing) || len(perf.got) != 1 {
		t.Errorf("a second automatic play: %v, %d performed", err, len(perf.got))
	}
	// ...but the owner asking to hear it again does.
	if err := f.svc.PlayBriefing(ctx, "c2", "b3", false, true); err != nil || len(perf.got) != 2 {
		t.Errorf("replay: %v", err)
	}

	mux := http.NewServeMux()
	f.svc.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/briefing")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET briefing: %v", err)
	}
	_ = res.Body.Close()

	f.now = f.now.Add(24 * time.Hour) // a new day: yesterday's isn't today's
	if err := f.svc.PlayBriefing(ctx, "c1", "b4", false, true); !errors.Is(err, ErrNoBriefing) {
		t.Errorf("next day: %v", err)
	}
	res, _ = http.Get(srv.URL + "/api/briefing")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("next day GET = %d", res.StatusCode)
	}
	_ = res.Body.Close()
}

func TestBriefingSchedule(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	next := f.svc.BriefingJobDef().Next
	f.now = time.Date(2026, 10, 20, 6, 0, 0, 0, f.loc)
	if got, ok := next(ctx, f.now, time.Time{}); !ok || !got.Equal(time.Date(2026, 10, 20, 7, 30, 0, 0, f.loc)) {
		t.Errorf("default = %v %v", got, ok)
	}
	if _, err := f.svc.Update(ctx, Patch{BriefingAt: str("06:45")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := next(ctx, f.now, time.Time{}); !got.Equal(time.Date(2026, 10, 20, 6, 45, 0, 0, f.loc)) {
		t.Errorf("moved = %v", got)
	}
	off := false
	if _, err := f.svc.Update(ctx, Patch{Briefing: &off}); err != nil {
		t.Fatal(err)
	}
	if _, ok := next(ctx, f.now, time.Time{}); ok {
		t.Error("a switched-off briefing is scheduled")
	}
}

func TestBriefingSurvivesAFailingSource(t *testing.T) {
	f := newFixture(t)
	f.svc.llm = &fakeModel{reply: "Morning."}
	f.svc.weather = fakeWeather{err: errors.New("down")}
	f.cal.err = errors.New("calendar down")
	f.svc.SetPerformer(&fakePerformer{})
	if _, err := f.svc.WriteBriefing(t.Context()); err != nil {
		t.Fatal(err)
	}
	in := f.svc.llm.(*fakeModel).asked[0].Messages[1].Content
	if !strings.Contains(in, "(The weather couldn't be checked.)") || !strings.Contains(in, "(The calendar couldn't be checked.)") {
		t.Errorf("input:\n%s", in)
	}
}

func TestSwitchingTheBriefingOffStopsTodaysToo(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	perf := &fakePerformer{}
	f.svc.llm = &fakeModel{reply: "Morning."}
	f.svc.SetPerformer(perf)
	if _, err := f.svc.WriteBriefing(ctx); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := f.svc.Update(ctx, Patch{Briefing: &off}); err != nil {
		t.Fatal(err)
	}
	if f.svc.BriefingWaiting(ctx) {
		t.Error("a switched-off briefing is still offered")
	}
	if err := f.svc.PlayBriefing(ctx, "c1", "b1", false, false); !errors.Is(err, ErrNoBriefing) || len(perf.got) != 0 {
		t.Errorf("automatic play when switched off: %v", err)
	}
	if err := f.svc.PlayBriefing(ctx, "c1", "b2", false, true); err != nil || len(perf.got) != 1 {
		t.Errorf("asked for: %v", err)
	}
}
