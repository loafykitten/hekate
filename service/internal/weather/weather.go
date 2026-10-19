// Package weather answers "what's it like outside?" with Open-Meteo (free, no
// key): geocoding, a cached forecast, the home place stored in settings, the
// agent tools and the REST route.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/loafykitten/hekate/service/internal/store"
)

const (
	homeKey     = "home_place"
	forecastTTL = 10 * time.Minute
	geocodeTTL  = 24 * time.Hour
)

// Settings is the part of the store the service uses: the settings table.
type Settings interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Options are the dependencies of a Service.
type Options struct {
	Settings Settings
	Units    string // "imperial" (default) or "metric"
	Location *time.Location
	Logger   *slog.Logger
	// HTTPClient and the URLs override the Open-Meteo client and endpoints (tests).
	HTTPClient  *http.Client
	GeocodeURL  string
	ForecastURL string
	// OnChange is called with "weather" after the home place changes.
	OnChange func(domain string)
	// Now overrides the clock for cache expiry (tests).
	Now func() time.Time
}

// Service reports the weather.
type Service struct {
	settings    Settings
	units       string
	loc         *time.Location
	log         *slog.Logger
	hc          *http.Client
	geocodeURL  string
	forecastURL string
	onChange    func(string)
	now         func() time.Time

	mu     sync.Mutex
	fcache map[string]cachedWeather
	geo    map[string]cachedPlaces
}

type cachedWeather struct {
	w   Weather
	exp time.Time
}

type cachedPlaces struct {
	p   []Place
	exp time.Time
}

// New builds a Service.
func New(o Options) *Service {
	s := &Service{
		settings: o.Settings, units: o.Units, loc: o.Location, log: o.Logger, hc: o.HTTPClient,
		geocodeURL: o.GeocodeURL, forecastURL: o.ForecastURL, onChange: o.OnChange, now: o.Now,
		fcache: map[string]cachedWeather{}, geo: map[string]cachedPlaces{},
	}
	if s.units != "metric" {
		s.units = "imperial"
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.hc == nil {
		s.hc = &http.Client{Timeout: 10 * time.Second}
	}
	if s.geocodeURL == "" {
		s.geocodeURL = defaultGeocodeURL
	}
	if s.forecastURL == "" {
		s.forecastURL = defaultForecastURL
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Weather is the card data of docs/PROTOCOL.md. Values the forecast model
// did not provide are null rather than invented.
type Weather struct {
	Place string `json:"place"`
	Unit  string `json:"unit"` // "F" or "C"
	Now   Now    `json:"now"`
	Today Today  `json:"today"`
	Hours []Hour `json:"hours"` // the next 12
	Days  []Day  `json:"days"`  // the 5 days after today
}

// Now is the current conditions.
type Now struct {
	Temp      int    `json:"temp"`
	FeelsLike int    `json:"feels_like"`
	Condition string `json:"condition"`
	Code      int    `json:"code"` // WMO weather code
}

// Today is today's summary. Sunrise and Sunset are RFC 3339.
type Today struct {
	High         *int   `json:"high"`
	Low          *int   `json:"low"`
	PrecipChance *int   `json:"precip_chance"`
	Sunrise      string `json:"sunrise"`
	Sunset       string `json:"sunset"`
}

// Hour is one hourly forecast entry. Time is RFC 3339.
type Hour struct {
	Time         string `json:"time"`
	Temp         *int   `json:"temp"`
	PrecipChance *int   `json:"precip_chance"`
	Code         int    `json:"code"`
}

// Day is one daily forecast entry. Date is YYYY-MM-DD.
type Day struct {
	Date         string `json:"date"`
	High         *int   `json:"high"`
	Low          *int   `json:"low"`
	PrecipChance *int   `json:"precip_chance"`
	Code         int    `json:"code"`
}

// ErrNoHome means no place was given and no home place is stored.
var ErrNoHome = errors.New("no home place is set")

// HomePlace returns the stored home place, or ErrNoHome.
func (s *Service) HomePlace(ctx context.Context) (Place, error) {
	if s.settings == nil {
		return Place{}, ErrNoHome
	}
	v, err := s.settings.GetSetting(ctx, homeKey)
	if errors.Is(err, store.ErrNotFound) {
		return Place{}, ErrNoHome
	}
	if err != nil {
		return Place{}, err
	}
	var p Place
	if err := json.Unmarshal([]byte(v), &p); err != nil || p.Name == "" {
		s.log.Warn("ignoring unreadable home_place setting")
		return Place{}, ErrNoHome
	}
	return p, nil
}

// SetHomePlace geocodes text, stores the best match as the home place and
// returns it with the other candidates.
func (s *Service) SetHomePlace(ctx context.Context, text string) (Place, []Place, error) {
	if s.settings == nil {
		return Place{}, nil, errors.New("settings are not available")
	}
	places, err := s.geocode(ctx, text)
	if err != nil {
		return Place{}, nil, err
	}
	b, err := json.Marshal(places[0])
	if err != nil {
		return Place{}, nil, err
	}
	if err := s.settings.SetSetting(ctx, homeKey, string(b)); err != nil {
		return Place{}, nil, err
	}
	if s.onChange != nil {
		s.onChange("weather")
	}
	return places[0], places[1:], nil
}

// Forecast returns the weather for the named place, or the home place when
// text is empty. Forecasts are cached for ten minutes per place.
func (s *Service) Forecast(ctx context.Context, text string) (Weather, error) {
	var p Place
	if strings.TrimSpace(text) == "" {
		var err error
		if p, err = s.HomePlace(ctx); err != nil {
			return Weather{}, err
		}
	} else {
		places, err := s.geocode(ctx, text)
		if err != nil {
			return Weather{}, err
		}
		p = places[0]
	}
	return s.forecastFor(ctx, p)
}

func (s *Service) forecastFor(ctx context.Context, p Place) (Weather, error) {
	key := fmt.Sprintf("%.4f,%.4f,%s,%s", p.Latitude, p.Longitude, s.units, p.Timezone)
	s.mu.Lock()
	if c, ok := s.fcache[key]; ok && s.now().Before(c.exp) {
		s.mu.Unlock()
		return withName(c.w, p.Name), nil
	}
	s.mu.Unlock()

	w, err := s.fetchForecast(ctx, p)
	if err != nil {
		return Weather{}, err
	}
	s.mu.Lock()
	s.fcache[key] = cachedWeather{w: w, exp: s.now().Add(forecastTTL)}
	s.mu.Unlock()
	return w, nil
}

// withName returns w labelled with name (the cache is keyed by coordinates, so
// two spellings of one place share an entry).
func withName(w Weather, name string) Weather {
	w.Place = name
	return w
}

func (s *Service) cachedGeocode(key string) ([]Place, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.geo[key]
	if !ok || !s.now().Before(c.exp) {
		return nil, false
	}
	return c.p, true
}

func (s *Service) storeGeocode(key string, p []Place) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.geo) > 200 {
		clear(s.geo)
	}
	s.geo[key] = cachedPlaces{p: p, exp: s.now().Add(geocodeTTL)}
}
