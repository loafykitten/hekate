// Package routines holds Renamon's proactive routines and their settings: the
// heartbeat that nudges the owner during waking hours when something needs
// them, and (next) the morning briefing.
package routines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/loafykitten/hekate/service/internal/config"
	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/jobs"
	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

// Invalidate domains: the routine settings, and today's briefing.
const (
	DomainRoutines = "routines"
	DomainBriefing = "briefing"
)

const settingsKey = "routines"

// Settings are the owner's choices for the routines, as GET /api/routines returns them.
type Settings struct {
	Heartbeat   bool   `json:"heartbeat"`    // nudges on or off
	WakingStart string `json:"waking_start"` // HH:MM: nudges only between these
	WakingEnd   string `json:"waking_end"`
	Briefing    bool   `json:"briefing"`    // the morning briefing on or off
	BriefingAt  string `json:"briefing_at"` // HH:MM: when it's written and pushed
}

// Defaults: nudges on, 08:00 to 22:00; the briefing at 07:30.
var defaults = Settings{Heartbeat: true, WakingStart: "08:00", WakingEnd: "22:00", Briefing: true, BriefingAt: "07:30"}

// Patch changes some settings; nil fields stay.
type Patch struct {
	Heartbeat   *bool   `json:"heartbeat"`
	WakingStart *string `json:"waking_start"`
	WakingEnd   *string `json:"waking_end"`
	Briefing    *bool   `json:"briefing"`
	BriefingAt  *string `json:"briefing_at"`
}

// Pusher delivers web push (with the Telegram fallback in production).
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Options are the dependencies of a Service.
type Options struct {
	Store     *store.Store
	Jobs      *jobs.Runner // woken when a schedule changes
	Tasks     TaskSource
	Calendar  EventSource
	Mail      MailSource
	FollowUps FollowUpSource
	Reminders ReminderSource
	Weather   WeatherSource
	LLM       Completer
	Model     config.Model // writes the briefing
	Tamer     string
	Gestures  func() []string // the gesture tags she may use
	Location  *time.Location
	Events    proto.Events
	Push      Pusher // nil: in-app notices only
	Logger    *slog.Logger
	Now       func() time.Time // tests replace the clock
}

// Service runs the routines and keeps their settings.
type Service struct {
	st        *store.Store
	jobs      *jobs.Runner
	tasks     TaskSource
	cal       EventSource
	mail      MailSource
	followUps FollowUpSource
	reminders ReminderSource
	weather   WeatherSource
	llm       Completer
	model     config.Model
	tamer     string
	gestures  func() []string
	performer Performer
	loc       *time.Location
	events    proto.Events
	pusher    Pusher
	log       *slog.Logger
	now       func() time.Time

	mu sync.Mutex // settings read-modify-write
}

// New builds the service.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("routines: Store is required")
	}
	s := &Service{st: o.Store, jobs: o.Jobs, tasks: o.Tasks, cal: o.Calendar, mail: o.Mail, followUps: o.FollowUps,
		reminders: o.Reminders, weather: o.Weather, llm: o.LLM, model: o.Model, tamer: o.Tamer, gestures: o.Gestures,
		loc: o.Location, events: o.Events, pusher: o.Push, log: o.Logger, now: o.Now}
	if s.tamer == "" {
		s.tamer = "your Tamer"
	}
	if s.gestures == nil {
		s.gestures = func() []string { return nil }
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.events == nil {
		s.events = proto.NopEvents{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Settings returns the current settings, defaults filling anything unset.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	out := defaults
	raw, err := s.st.GetSetting(ctx, settingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.log.Warn("routine settings unreadable, using defaults", "error", err)
		return defaults, nil
	}
	return out, nil
}

// Update applies a patch and wakes the routines whose schedule may have changed.
func (s *Service) Update(ctx context.Context, p Patch) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if p.Heartbeat != nil {
		cur.Heartbeat = *p.Heartbeat
	}
	if p.Briefing != nil {
		cur.Briefing = *p.Briefing
	}
	for _, f := range []struct {
		in  *string
		out *string
	}{{p.WakingStart, &cur.WakingStart}, {p.WakingEnd, &cur.WakingEnd}, {p.BriefingAt, &cur.BriefingAt}} {
		if f.in == nil {
			continue
		}
		c, err := jobs.ParseClock(*f.in)
		if err != nil {
			return Settings{}, httpx.BadRequest("%s", err)
		}
		*f.out = c.String()
	}
	if cur.WakingStart == cur.WakingEnd {
		return Settings{}, httpx.BadRequest("waking hours need to start and end at different times")
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return Settings{}, fmt.Errorf("encode routine settings: %w", err)
	}
	if err := s.st.SetSetting(ctx, settingsKey, string(raw)); err != nil {
		return Settings{}, err
	}
	if s.jobs != nil {
		s.jobs.Wake(HeartbeatJob)
		s.jobs.Wake(BriefingJob)
	}
	s.events.Invalidate(DomainRoutines)
	return cur, nil
}

// awake reports whether t falls in the waking hours (which may run past midnight).
func (st Settings) awake(t time.Time, loc *time.Location) bool {
	start, err1 := jobs.ParseClock(st.WakingStart)
	end, err2 := jobs.ParseClock(st.WakingEnd)
	if err1 != nil || err2 != nil {
		start, _ = jobs.ParseClock(defaults.WakingStart)
		end, _ = jobs.ParseClock(defaults.WakingEnd)
	}
	t = t.In(loc)
	m := t.Hour()*60 + t.Minute()
	a, b := start.Hour*60+start.Minute, end.Hour*60+end.Minute
	if a < b {
		return m >= a && m < b
	}
	return m >= a || m < b
}

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/routines", s.handleGet)
	mux.HandleFunc("PATCH /api/routines", s.handlePatch)
	mux.HandleFunc("GET /api/briefing", s.handleBriefing)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	st, err := s.Settings(r.Context())
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if err := httpx.Decode(w, r, &p); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	st, err := s.Update(r.Context(), p)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}
