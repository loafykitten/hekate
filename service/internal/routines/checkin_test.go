package routines

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/calendar"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/memory"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/tasks"
)

func checkInTool(t *testing.T, f *fixture) func() (string, []proto.Card) {
	t.Helper()
	tool := f.svc.Tools()[2]
	if tool.Name != CheckInTool {
		t.Fatalf("tool = %q", tool.Name)
	}
	return func() (string, []proto.Card) {
		res, err := tool.Run(t.Context(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return res.Content, res.Cards
	}
}

func TestTheCheckInAsksOnceADay(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.now = time.Date(2026, 10, 21, 0, 0, 0, 0, f.loc) // midnight: still Tuesday's check-in

	f.rec.failPush = true // no app open, no push delivered: nothing counts as asked
	if got, err := f.svc.AskCheckIn(ctx); err != nil || !strings.HasPrefix(got, "Couldn't reach") {
		t.Fatalf("unreached = %q, %v", got, err)
	}
	f.rec.failPush = false
	if got, err := f.svc.AskCheckIn(ctx); err != nil || got != "Asked how the day went" {
		t.Fatalf("ask = %q, %v", got, err)
	}
	p := f.rec.pushes[len(f.rec.pushes)-1]
	if p.Title != "How did today go?" || p.URL != "/?checkin=2026-10-20" || p.Tag != "checkin" || !p.NoBackup {
		t.Errorf("push = %+v", p)
	}
	n := f.rec.notices[len(f.rec.notices)-1]
	if n.Kind != proto.NotifyKindCheckIn || n.ID != "checkin-2026-10-20" || n.Title != "How did today go?" {
		t.Errorf("notice = %+v", n)
	}

	f.now = f.now.Add(10 * time.Minute) // asked again (a restart, a manual run): never twice
	if got, _ := f.svc.AskCheckIn(ctx); got != "Already asked about the day" || len(f.rec.pushes) != 2 {
		t.Errorf("again = %q, pushes %d", got, len(f.rec.pushes))
	}
	f.now = time.Date(2026, 10, 22, 0, 0, 0, 0, f.loc) // the next night
	if got, _ := f.svc.AskCheckIn(ctx); got != "Asked how the day went" || f.rec.pushes[2].URL != "/?checkin=2026-10-21" {
		t.Errorf("next night = %q, %+v", got, f.rec.pushes)
	}
}

func TestNoCheckInAfterTheDayWasTalkedOver(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.now = time.Date(2026, 10, 20, 22, 40, 0, 0, f.loc)
	checkInTool(t, f)() // the owner started talking the day over themselves
	f.now = time.Date(2026, 10, 21, 0, 0, 0, 0, f.loc)
	if got, _ := f.svc.AskCheckIn(ctx); got != "Already talked the day over" || len(f.rec.pushes)+len(f.rec.notices) != 0 {
		t.Errorf("ask = %q, pushes %v, notices %v", got, f.rec.pushes, f.rec.notices)
	}
}

func TestTheCheckInDayTurnsAtSixLocal(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 20, 23, 59, 0, 0, f.loc), "2026-10-20"},
		{time.Date(2026, 10, 21, 0, 0, 0, 0, f.loc), "2026-10-20"},
		{time.Date(2026, 10, 21, 5, 59, 0, 0, f.loc), "2026-10-20"},
		{time.Date(2026, 10, 21, 6, 0, 0, 0, f.loc), "2026-10-21"},
		// The clocks go back at 02:00 on Nov 1 and forward at 02:00 on Mar 14.
		{time.Date(2026, 11, 1, 5, 30, 0, 0, f.loc), "2026-10-31"},
		{time.Date(2026, 11, 1, 6, 0, 0, 0, f.loc), "2026-11-01"},
		{time.Date(2027, 3, 14, 6, 30, 0, 0, f.loc), "2027-03-14"},
	} {
		if got := f.svc.talkedOverDay(tt.at); got != tt.want {
			t.Errorf("at %v: day %s, want %s", tt.at, got, tt.want)
		}
	}
}

func TestCheckInSettings(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if st, _ := f.svc.Settings(ctx); !st.CheckIn || st.CheckInAt != "00:00" {
		t.Errorf("defaults = %+v", st)
	}
	if c, ok := f.svc.checkInAt(ctx); !ok || c != (jobs.Clock{}) {
		t.Errorf("scheduled at %v, %v", c, ok)
	}
	off, at := false, "23:30"
	st, err := f.svc.Update(ctx, Patch{CheckIn: &off, CheckInAt: &at})
	if err != nil || st.CheckIn || st.CheckInAt != "23:30" || !st.Heartbeat {
		t.Fatalf("update = %+v, %v", st, err)
	}
	if _, ok := f.svc.checkInAt(ctx); ok {
		t.Error("a check-in switched off is still scheduled")
	}
	if got, _ := f.svc.AskCheckIn(ctx); got != "The check-in is off" || len(f.rec.pushes) != 0 {
		t.Errorf("ask while off = %q", got)
	}
	bad := "25:00"
	if _, err := f.svc.Update(ctx, Patch{CheckInAt: &bad}); err == nil {
		t.Error("a bad check-in time was accepted")
	}
	res, err := f.svc.Tools()[0].Run(ctx, json.RawMessage(`{"checkin":true}`))
	if err != nil || !strings.Contains(res.Content, "The evening check-in is on, at 23:30") {
		t.Errorf("routine_settings = %q, %v", res.Content, err)
	}
}

func TestTappingTheCheckInOpensIt(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.now = time.Date(2026, 10, 21, 0, 10, 0, 0, f.loc)
	open := func(day string) func(bool) {
		op, ok := f.svc.CheckIn(ctx, day)
		if !ok {
			return nil
		}
		if op.Tool != CheckInTool || op.Settle == nil {
			t.Fatalf("opening = %+v", op)
		}
		return op.Settle
	}
	if open("2026-10-19") != nil {
		t.Error("an old notice started a check-in")
	}
	settle := open("2026-10-20")
	if settle == nil {
		t.Fatal("the night's notice didn't start the check-in")
	}
	if open("") != nil {
		t.Error("a second check-in started while the first was under way")
	}
	run := checkInTool(t, f)
	run()
	settle(false) // failed or cut short: the day can be tried again
	if settle = open(""); settle == nil {
		t.Fatal("a failed check-in can't be tried again")
	}
	run()
	settle(true)
	if open("2026-10-20") != nil || open("") != nil {
		t.Error("a day talked over started another check-in")
	}
	f.now = time.Date(2026, 10, 21, 23, 0, 0, 0, f.loc) // Wednesday evening
	if open("") == nil {
		t.Error("the next day's check-in didn't start")
	}
}

func TestTheCheckInGathersTheDay(t *testing.T) {
	f := newFixture(t)
	f.now = time.Date(2026, 10, 21, 0, 30, 0, 0, f.loc)
	at := func(d, h, m int) string { return time.Date(2026, 10, d, h, m, 0, 0, f.loc).Format(time.RFC3339) }
	f.tasks.done = []tasks.Task{
		{ID: "2", Title: "Post office"}, {ID: "1", Title: "Dishes", RRule: str("FREQ=DAILY")}, {ID: "1", Title: "Dishes", RRule: str("FREQ=DAILY")},
	}
	f.tasks.list = []tasks.Task{
		{ID: "3", Title: "Pay rent", Due: str("2026-10-19")},
		{ID: "4", Title: "Call the bank", Due: str(at(20, 15, 0))},
		{ID: "5", Title: "Dentist forms", Due: str("2026-10-21")},
	}
	f.cal.inRange = true
	f.cal.events = []calendar.Event{
		{UID: "l", Title: "Lunch with Sam", Start: at(20, 12, 0), End: at(20, 13, 0)},
		{UID: "c", Title: "Early call", Start: at(21, 10, 0), End: at(21, 10, 30)},
		{UID: "w", Title: "Next week", Start: at(28, 10, 0), End: at(28, 11, 0)},
	}
	f.followUps.due = []memory.Memory{{ID: "6", Content: "Ask how the interview went"}}

	out, cards := checkInTool(t, f)()
	for _, want := range []string{
		"This is the evening check-in", "Looking back over Tuesday, October 20. It's Wed 00:30 now.",
		"## Done today\n- Post office\n- Dishes\n",
		"## Still open, due today or earlier\n- Pay rent (due 2026-10-19)\n- Call the bank (due " + at(20, 15, 0) + ")\n",
		"## Tasks due tomorrow\n- Dentist forms (due 2026-10-21)\n",
		"## Today's calendar\n- 12:00–13:00 Lunch with Sam\n",
		"## Coming up next, the next 24 hours\n- 10:00–10:30 Early call\n",
		"Ask how the interview went",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Next week") {
		t.Error("an event a week out is in the check-in")
	}
	var titles []string
	for _, c := range cards {
		titles = append(titles, c.Title)
	}
	// Most important last: the app shows it on top.
	if strings.Join(titles, ",") != "Still open,Done today" {
		t.Errorf("cards = %v", titles)
	}
	var done struct {
		Items  []tasks.Task
		Record bool
	}
	if err := json.Unmarshal(cards[1].Data, &done); err != nil || len(done.Items) != 2 || !done.Record {
		t.Errorf("done card = %+v, %v: want two items, as a record", done, err)
	}
}
