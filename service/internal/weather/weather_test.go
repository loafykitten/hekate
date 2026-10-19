package weather

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/store"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func (m *memSettings) GetSetting(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.m[key]; ok {
		return v, nil
	}
	return "", store.ErrNotFound
}

func (m *memSettings) SetSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]string{}
	}
	m.m[key] = value
	return nil
}

type fake struct {
	srv       *httptest.Server
	geoCalls  atomic.Int32
	fcCalls   atomic.Int32
	lastGeo   atomic.Pointer[url.Values]
	lastFc    atomic.Pointer[url.Values]
	fcStatus  int
	fcMutate  func(map[string]any)
	geoStatus int
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		f.geoCalls.Add(1)
		q := r.URL.Query()
		f.lastGeo.Store(&q)
		if f.geoStatus != 0 {
			w.WriteHeader(f.geoStatus)
			return
		}
		if q.Get("name") == "Nowhereville" {
			_, _ = w.Write([]byte(`{"generationtime_ms":0.2}`))
			return
		}
		_, _ = w.Write(read(t, "geocode_austin.json"))
	})
	mux.HandleFunc("/v1/forecast", func(w http.ResponseWriter, r *http.Request) {
		f.fcCalls.Add(1)
		q := r.URL.Query()
		f.lastFc.Store(&q)
		if f.fcStatus != 0 {
			w.WriteHeader(f.fcStatus)
			_, _ = w.Write([]byte(`{"error":true,"reason":"Latitude must be in range of -90 to 90°."}`))
			return
		}
		if f.fcMutate == nil {
			_, _ = w.Write(read(t, "forecast_austin.json"))
			return
		}
		var m map[string]any
		_ = json.Unmarshal(read(t, "forecast_austin.json"), &m)
		f.fcMutate(m)
		_ = json.NewEncoder(w).Encode(m)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) service(t *testing.T, units string, st Settings, clock func() time.Time) *Service {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Settings: st, Units: units, Location: loc,
		GeocodeURL: f.srv.URL + "/v1/search", ForecastURL: f.srv.URL + "/v1/forecast", Now: clock,
	})
}

func TestForecastShape(t *testing.T) {
	f := newFake(t)
	s := f.service(t, "imperial", &memSettings{}, nil)
	w, err := s.Forecast(context.Background(), "Austin")
	if err != nil {
		t.Fatal(err)
	}
	if w.Place != "Austin, Texas" || w.Unit != "F" {
		t.Errorf("place/unit = %q %q", w.Place, w.Unit)
	}
	if w.Now != (Now{Temp: 68, FeelsLike: 66, Condition: "Partly cloudy", Code: 2}) {
		t.Errorf("now = %+v", w.Now)
	}
	if *w.Today.High != 78 || *w.Today.Low != 51 || *w.Today.PrecipChance != 20 {
		t.Errorf("today = %+v", w.Today)
	}
	if w.Today.Sunrise != "2026-10-19T07:35:00-05:00" || w.Today.Sunset != "2026-10-19T18:55:00-05:00" {
		t.Errorf("sun = %q %q", w.Today.Sunrise, w.Today.Sunset)
	}
	if len(w.Hours) != 12 || w.Hours[0].Time != "2026-10-19T09:00:00-05:00" || w.Hours[11].Time != "2026-10-19T20:00:00-05:00" {
		t.Fatalf("hours = %d, first %q last %q", len(w.Hours), w.Hours[0].Time, w.Hours[len(w.Hours)-1].Time)
	}
	if w.Hours[11].Code != 61 || *w.Hours[11].PrecipChance != 10 || *w.Hours[0].PrecipChance != 0 {
		t.Errorf("hours = %+v ... %+v", w.Hours[0], w.Hours[11])
	}
	if len(w.Days) != 5 || w.Days[0].Date != "2026-10-20" || w.Days[4].Date != "2026-10-24" {
		t.Fatalf("days = %+v", w.Days)
	}
	if w.Days[1].Code != 95 || *w.Days[1].High != 75 || w.Days[3].PrecipChance != nil {
		// day index 3 is 2026-10-23 (code 71) with no precipitation probability
		t.Errorf("days = %+v", w.Days)
	}
	if w.Days[3].Code != 71 {
		t.Errorf("day 3 = %+v", w.Days[3])
	}

	// The request asked for what the card needs, in the place's zone.
	q := f.lastFc.Load()
	if q.Get("temperature_unit") != "fahrenheit" || q.Get("wind_speed_unit") != "mph" || q.Get("timezone") != "America/Chicago" ||
		q.Get("forecast_days") != "6" || !strings.Contains(q.Get("daily"), "sunrise") || !strings.Contains(q.Get("hourly"), "precipitation_probability") ||
		!strings.Contains(q.Get("current"), "apparent_temperature") || q.Get("latitude") != "30.2672" {
		t.Errorf("forecast query = %v", *q)
	}
	g := f.lastGeo.Load()
	if g.Get("name") != "Austin" || g.Get("count") != "10" {
		t.Errorf("geocode query = %v", *g)
	}
}

func TestCardJSONMatchesProtocol(t *testing.T) {
	f := newFake(t)
	s := f.service(t, "imperial", &memSettings{}, nil)
	w, err := s.Forecast(context.Background(), "Austin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(w)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	keys := func(v any) []string {
		var out []string
		for k := range v.(map[string]any) {
			out = append(out, k)
		}
		return out
	}
	has := func(v any, want ...string) {
		t.Helper()
		got := map[string]bool{}
		for _, k := range keys(v) {
			got[k] = true
		}
		if len(got) != len(want) {
			t.Errorf("keys = %v, want %v", keys(v), want)
		}
		for _, k := range want {
			if !got[k] {
				t.Errorf("missing %q in %v", k, keys(v))
			}
		}
	}
	has(m, "place", "unit", "now", "today", "hours", "days")
	has(m["now"], "temp", "feels_like", "condition", "code")
	has(m["today"], "high", "low", "precip_chance", "sunrise", "sunset")
	has(m["hours"].([]any)[0], "time", "temp", "precip_chance", "code")
	has(m["days"].([]any)[0], "date", "high", "low", "precip_chance", "code")
}

func TestMetricUnits(t *testing.T) {
	f := newFake(t)
	s := f.service(t, "metric", &memSettings{}, nil)
	w, err := s.Forecast(context.Background(), "Austin")
	if err != nil {
		t.Fatal(err)
	}
	q := f.lastFc.Load()
	if q.Get("temperature_unit") != "celsius" || q.Get("wind_speed_unit") != "kmh" || w.Unit != "C" {
		t.Errorf("unit=%q query=%v", w.Unit, *q)
	}
}

func TestGeocodeHints(t *testing.T) {
	f := newFake(t)
	s := f.service(t, "imperial", &memSettings{}, nil)
	tests := []struct {
		text string
		want string
	}{
		{"Austin", "Austin, Texas"},
		{"austin, minnesota", "Austin, Minnesota"},
		{"Austin, AU", "Austin, Victoria"},
		{"Austin, Australia", "Austin, Victoria"},
		{"Austin, Texas, US", "Austin, Texas"},
	}
	for _, tt := range tests {
		ps, err := s.geocode(context.Background(), tt.text)
		if err != nil || ps[0].Name != tt.want {
			t.Errorf("%q -> %v, %v; want %q", tt.text, ps, err, tt.want)
		}
	}
	if g := f.lastGeo.Load(); g.Get("name") != "Austin" {
		t.Errorf("name sent = %q; the state must not be sent to the name search", g.Get("name"))
	}
	if _, err := s.geocode(context.Background(), "Austin, Narnia"); err == nil {
		t.Error("unmatched hint accepted")
	}
	if _, err := s.geocode(context.Background(), "Nowhereville"); err == nil || !strings.Contains(err.Error(), "nothing found") {
		t.Errorf("unknown place err = %v", err)
	}
	if _, err := s.geocode(context.Background(), "  "); err == nil {
		t.Error("blank accepted")
	}
	if _, err := s.geocode(context.Background(), strings.Repeat("x", 200)); err == nil {
		t.Error("long text accepted")
	}
}

func TestHomePlaceStoredInSettings(t *testing.T) {
	f := newFake(t)
	st := &memSettings{}
	var changes []string
	s := f.service(t, "imperial", st, nil)
	s.onChange = func(d string) { changes = append(changes, d) }

	if _, err := s.HomePlace(context.Background()); !errors.Is(err, ErrNoHome) {
		t.Fatalf("empty home: %v", err)
	}
	if _, err := s.Forecast(context.Background(), ""); !errors.Is(err, ErrNoHome) {
		t.Errorf("forecast without home: %v", err)
	}
	if line, err := s.PromptContext(context.Background(), time.Now()); line != "" || err != nil {
		t.Errorf("prompt without home = %q, %v", line, err)
	}

	p, others, err := s.SetHomePlace(context.Background(), "Austin")
	if err != nil || p.Name != "Austin, Texas" || len(others) != 2 {
		t.Fatalf("set = %+v %v %v", p, others, err)
	}
	if len(changes) != 1 {
		t.Errorf("changes = %v", changes)
	}
	var stored Place
	if err := json.Unmarshal([]byte(st.m["home_place"]), &stored); err != nil {
		t.Fatal(err)
	}
	if stored != (Place{Name: "Austin, Texas", Latitude: 30.26715, Longitude: -97.74306, Timezone: "America/Chicago"}) {
		t.Errorf("stored = %+v", stored)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(st.m["home_place"]), &raw)
	if len(raw) != 4 {
		t.Errorf("stored keys = %v, want exactly name, latitude, longitude, timezone", raw)
	}
	w, err := s.Forecast(context.Background(), "")
	if err != nil || w.Place != "Austin, Texas" {
		t.Errorf("home forecast = %+v, %v", w.Place, err)
	}

	st.m["home_place"] = "{broken"
	if _, err := s.HomePlace(context.Background()); !errors.Is(err, ErrNoHome) {
		t.Errorf("corrupt setting: %v", err)
	}
}

func TestForecastCache(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 10, 19, 9, 45, 0, 0, time.UTC)
	s := f.service(t, "imperial", &memSettings{}, func() time.Time { return now })
	ctx := context.Background()

	if _, err := s.Forecast(ctx, "Austin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Forecast(ctx, "austin"); err != nil { // same place, other spelling
		t.Fatal(err)
	}
	if f.fcCalls.Load() != 1 || f.geoCalls.Load() != 1 {
		t.Errorf("calls after repeat: forecast %d geocode %d, want 1 each", f.fcCalls.Load(), f.geoCalls.Load())
	}
	now = now.Add(9*time.Minute + 59*time.Second)
	_, _ = s.Forecast(ctx, "Austin")
	if f.fcCalls.Load() != 1 {
		t.Errorf("refetched inside the ttl")
	}
	now = now.Add(2 * time.Second)
	_, _ = s.Forecast(ctx, "Austin")
	if f.fcCalls.Load() != 2 {
		t.Errorf("not refetched after 10 minutes: %d", f.fcCalls.Load())
	}
	// A different place is a different entry.
	if _, err := s.Forecast(ctx, "Austin, Minnesota"); err != nil {
		t.Fatal(err)
	}
	if f.fcCalls.Load() != 3 {
		t.Errorf("other place shared a cache entry: %d", f.fcCalls.Load())
	}
	// The cache is per unit system.
	m := f.service(t, "metric", &memSettings{}, func() time.Time { return now })
	_, _ = m.Forecast(ctx, "Austin")
	if f.fcCalls.Load() != 4 {
		t.Errorf("services share a cache")
	}
	// Failures are not cached.
	f.fcStatus = 500
	now = now.Add(time.Hour)
	if _, err := s.Forecast(ctx, "Austin"); err == nil {
		t.Fatal("error swallowed")
	}
	f.fcStatus = 0
	if _, err := s.Forecast(ctx, "Austin"); err != nil {
		t.Errorf("after recovery: %v", err)
	}
}

func TestUpstreamErrors(t *testing.T) {
	f := newFake(t)
	s := f.service(t, "imperial", &memSettings{}, nil)
	f.geoStatus = 503
	if _, err := s.Forecast(context.Background(), "Austin"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("geocode 503: %v", err)
	}
	f.geoStatus = 0
	f.fcStatus = 400
	if _, err := s.Forecast(context.Background(), "Austin"); err == nil || !strings.Contains(err.Error(), "Latitude must be") {
		t.Errorf("forecast 400: %v", err)
	}
	f.fcStatus = 0
	f.fcMutate = func(m map[string]any) { delete(m, "current") }
	if _, err := s.Forecast(context.Background(), "Austin"); err == nil || !strings.Contains(err.Error(), "no current") {
		t.Errorf("no current: %v", err)
	}
	f.fcMutate = func(m map[string]any) { m["current"].(map[string]any)["time"] = "yesterday" }
	if _, err := s.Forecast(context.Background(), "Austin"); err == nil || !strings.Contains(err.Error(), "bad time") {
		t.Errorf("bad time: %v", err)
	}
	dead := New(Options{GeocodeURL: "http://127.0.0.1:1/", ForecastURL: "http://127.0.0.1:1/"})
	if _, err := dead.Forecast(context.Background(), "Austin"); err == nil {
		t.Error("unreachable service gave no error")
	}
}

func TestMissingValuesStayNull(t *testing.T) {
	f := newFake(t)
	f.fcMutate = func(m map[string]any) {
		h := m["hourly"].(map[string]any)
		h["precipitation_probability"] = nil
		h["temperature_2m"].([]any)[9] = nil // 09:00, the first hour shown
		m["current"].(map[string]any)["apparent_temperature"] = nil
	}
	s := f.service(t, "imperial", &memSettings{}, nil)
	w, err := s.Forecast(context.Background(), "Austin")
	if err != nil {
		t.Fatal(err)
	}
	if w.Hours[0].Temp != nil || w.Hours[0].PrecipChance != nil || w.Hours[1].Temp == nil {
		t.Errorf("hours = %+v %+v", w.Hours[0], w.Hours[1])
	}
	if w.Now.FeelsLike != w.Now.Temp {
		t.Errorf("feels like without data = %d, want the temperature %d", w.Now.FeelsLike, w.Now.Temp)
	}
	b, _ := json.Marshal(w.Hours[0])
	if !strings.Contains(string(b), `"temp":null`) {
		t.Errorf("json = %s", b)
	}
}

func TestCondition(t *testing.T) {
	tests := map[int]string{
		0: "Clear", 1: "Mostly clear", 2: "Partly cloudy", 3: "Overcast", 45: "Fog", 48: "Freezing fog",
		51: "Light drizzle", 53: "Drizzle", 55: "Heavy drizzle", 56: "Light freezing drizzle", 57: "Freezing drizzle",
		61: "Light rain", 63: "Rain", 65: "Heavy rain", 66: "Light freezing rain", 67: "Freezing rain",
		71: "Light snow", 73: "Snow", 75: "Heavy snow", 77: "Snow grains",
		80: "Light rain showers", 81: "Rain showers", 82: "Heavy rain showers", 85: "Light snow showers", 86: "Heavy snow showers",
		95: "Thunderstorm", 96: "Thunderstorm with hail", 99: "Thunderstorm with heavy hail",
		4: "Unknown", -1: "Unknown", 100: "Unknown",
	}
	for code, want := range tests {
		if got := Condition(code); got != want {
			t.Errorf("Condition(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestToolsAndPrompt(t *testing.T) {
	f := newFake(t)
	st := &memSettings{}
	s := f.service(t, "imperial", st, nil)
	ctx := context.Background()
	tools := map[string]func(string) (string, *struct{ Kind, Title string }, error){}
	for _, tl := range s.Tools() {
		tl := tl
		tools[tl.Name] = func(args string) (string, *struct{ Kind, Title string }, error) {
			r, err := tl.Run(ctx, json.RawMessage(args))
			if r.Card != nil {
				return r.Content, &struct{ Kind, Title string }{r.Card.Kind, r.Card.Title}, err
			}
			return r.Content, nil, err
		}
	}
	if _, ok := tools["weather"]; !ok || tools["set_home_place"] == nil {
		t.Fatalf("tools = %v", tools)
	}

	// No home place and no argument: ask the user.
	content, card, err := tools["weather"](`{}`)
	if err != nil || card != nil || !strings.Contains(content, "Ask them") {
		t.Errorf("no home: %q %v %v", content, card, err)
	}

	content, _, err = tools["set_home_place"](`{"place":"Austin"}`)
	if err != nil || !strings.Contains(content, "Austin, Texas") || !strings.Contains(content, "America/Chicago") || !strings.Contains(content, "Austin, Minnesota") {
		t.Errorf("set_home_place: %q %v", content, err)
	}
	if _, _, err := tools["set_home_place"](`{"place":"Nowhereville"}`); err == nil || !strings.Contains(err.Error(), "nothing found") {
		t.Errorf("unknown place: %v", err)
	}

	content, card, err = tools["weather"](`{}`)
	if err != nil || card == nil || card.Kind != "weather" {
		t.Fatalf("weather: %q %v %v", content, card, err)
	}
	for _, want := range []string{"Austin, Texas", "68°F", "Partly cloudy", "high 78°", "sunrise 07:35", "Next hours:", "09:00 61°", "Next days:", "Tue Oct 20"} {
		if !strings.Contains(content, want) {
			t.Errorf("content lacks %q:\n%s", want, content)
		}
	}
	content, card, err = tools["weather"](`{"place":"Austin, Minnesota"}`)
	if err != nil || !strings.Contains(content, "Austin, Minnesota") || card == nil {
		t.Errorf("weather(place): %q %v", content, err)
	}

	line, err := s.PromptContext(ctx, time.Now())
	want := "Weather in Austin, Texas: partly cloudy, 68°F now (feels like 66°F); today high 78°F, low 51°F, 20% chance of precipitation."
	if err != nil || line != want {
		t.Errorf("prompt = %q, %v\nwant     %q", line, err, want)
	}
	if strings.Contains(line, "\n") {
		t.Error("prompt context must be one line")
	}
}

func TestREST(t *testing.T) {
	f := newFake(t)
	st := &memSettings{}
	s := f.service(t, "imperial", st, nil)
	mux := http.NewServeMux()
	s.Register(mux)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	rec := get("/api/weather")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("no home: %d %s", rec.Code, rec.Body)
	}
	rec = get("/api/weather?place=Nowhereville")
	if rec.Code != 404 {
		t.Errorf("unknown place: %d %s", rec.Code, rec.Body)
	}
	rec = get("/api/weather?place=" + url.QueryEscape(strings.Repeat("x", 300)))
	if rec.Code != 400 {
		t.Errorf("long place: %d %s", rec.Code, rec.Body)
	}
	rec = get("/api/weather?place=Austin")
	var w Weather
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &w) != nil || w.Place != "Austin, Texas" || len(w.Hours) != 12 {
		t.Errorf("place: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
	}
	_, _, _ = s.SetHomePlace(context.Background(), "Austin")
	if rec = get("/api/weather"); rec.Code != 200 {
		t.Errorf("home: %d %s", rec.Code, rec.Body)
	}
	f.fcStatus = 500
	now := s.now().Add(time.Hour)
	s.now = func() time.Time { return now }
	if rec = get("/api/weather"); rec.Code != 502 || strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Errorf("upstream failure: %d %s", rec.Code, rec.Body)
	}
}

// Live check against the real Open-Meteo (free, no key). Gated so CI stays
// offline: HEKATE_LIVE_TESTS=1 go test -run Live ./internal/weather/
func TestLiveOpenMeteo(t *testing.T) {
	if os.Getenv("HEKATE_LIVE_TESTS") != "1" {
		t.Skip("set HEKATE_LIVE_TESTS=1 to run")
	}
	s := New(Options{Settings: &memSettings{}, Units: "imperial"})
	w, err := s.Forecast(context.Background(), "Austin, Texas")
	if err != nil {
		t.Fatal(err)
	}
	if w.Place != "Austin, Texas" || len(w.Hours) != 12 || len(w.Days) != 5 || w.Today.High == nil || w.Today.Sunrise == "" {
		t.Fatalf("weather = %+v", w)
	}
	t.Logf("%s: %d°%s, %s; today %d/%d", w.Place, w.Now.Temp, w.Unit, w.Now.Condition, *w.Today.High, *w.Today.Low)
}
