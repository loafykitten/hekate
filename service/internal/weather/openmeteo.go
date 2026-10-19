package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultGeocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	defaultForecastURL = "https://api.open-meteo.com/v1/forecast"
	maxAPIBody         = 4 << 20
)

// Place is a resolved location. It is also the JSON stored for the home place.
type Place struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

var (
	errNoPlace  = errors.New("no such place")
	errBadPlace = errors.New("invalid place")
)

// get fetches a JSON document with a size limit and decodes it into v.
func (s *Service) get(ctx context.Context, endpoint string, q url.Values, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("weather service request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return fmt.Errorf("weather service response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Reason != "" {
			return fmt.Errorf("weather service answered %d: %s", resp.StatusCode, e.Reason)
		}
		return fmt.Errorf("weather service answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("weather service response: %w", err)
	}
	return nil
}

type geoResult struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Admin1      string  `json:"admin1"`
}

func (g geoResult) display() string {
	switch {
	case g.Admin1 != "" && !strings.EqualFold(g.Admin1, g.Name):
		return g.Name + ", " + g.Admin1
	case g.Country != "":
		return g.Name + ", " + g.Country
	}
	return g.Name
}

func (g geoResult) place() Place {
	return Place{Name: g.display(), Latitude: g.Latitude, Longitude: g.Longitude, Timezone: g.Timezone}
}

// matches reports whether every comma-separated hint (a state, country or
// country code) names this result.
func (g geoResult) matches(hints []string) bool {
	for _, h := range hints {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if h != strings.ToLower(g.Admin1) && h != strings.ToLower(g.Country) && h != strings.ToLower(g.CountryCode) {
			return false
		}
	}
	return true
}

// geocode resolves free text such as "Austin", "Austin, Texas" or "Paris, FR"
// to places, best first. Open-Meteo matches only the name, so text after the
// first comma narrows the candidates instead.
func (s *Service) geocode(ctx context.Context, text string) ([]Place, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil, fmt.Errorf("%w: place is required", errBadPlace)
	}
	if len([]rune(text)) > 100 {
		return nil, fmt.Errorf("%w: place is too long", errBadPlace)
	}
	key := strings.ToLower(text)
	if p, ok := s.cachedGeocode(key); ok {
		return p, nil
	}
	name, rest, _ := strings.Cut(text, ",")
	var hints []string
	if rest != "" {
		hints = strings.Split(rest, ",")
	}
	q := url.Values{}
	q.Set("name", strings.TrimSpace(name))
	q.Set("count", "10")
	q.Set("language", "en")
	q.Set("format", "json")
	var resp struct {
		Results []geoResult `json:"results"`
	}
	if err := s.get(ctx, s.geocodeURL, q, &resp); err != nil {
		return nil, err
	}
	var places []Place
	for _, r := range resp.Results {
		if r.Name == "" || r.Latitude < -90 || r.Latitude > 90 || r.Longitude < -180 || r.Longitude > 180 {
			continue
		}
		if r.matches(hints) {
			places = append(places, r.place())
		}
	}
	if len(places) == 0 {
		return nil, fmt.Errorf("%w: nothing found called %q", errNoPlace, text)
	}
	s.storeGeocode(key, places)
	return places, nil
}

type forecastResponse struct {
	UTCOffset int    `json:"utc_offset_seconds"`
	Timezone  string `json:"timezone"`
	Current   struct {
		Time          string   `json:"time"`
		Temperature   *float64 `json:"temperature_2m"`
		Apparent      *float64 `json:"apparent_temperature"`
		Code          *int     `json:"weather_code"`
		IsDay         int      `json:"is_day"`
		Precipitation *float64 `json:"precipitation"`
	} `json:"current"`
	Hourly struct {
		Time   []string   `json:"time"`
		Temp   []*float64 `json:"temperature_2m"`
		Precip []*float64 `json:"precipitation_probability"`
		Code   []*int     `json:"weather_code"`
	} `json:"hourly"`
	Daily struct {
		Time    []string   `json:"time"`
		Code    []*int     `json:"weather_code"`
		High    []*float64 `json:"temperature_2m_max"`
		Low     []*float64 `json:"temperature_2m_min"`
		Precip  []*float64 `json:"precipitation_probability_max"`
		Sunrise []string   `json:"sunrise"`
		Sunset  []string   `json:"sunset"`
	} `json:"daily"`
}

const (
	hoursShown = 12
	daysShown  = 5
)

// fetchForecast fetches and shapes the weather card data for p.
func (s *Service) fetchForecast(ctx context.Context, p Place) (Weather, error) {
	tz := p.Timezone
	if tz == "" {
		tz = s.loc.String()
	}
	if tz == "" || tz == "Local" {
		tz = "auto"
	}
	imperial := s.units != "metric"
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", p.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", p.Longitude))
	q.Set("current", "temperature_2m,apparent_temperature,weather_code,is_day")
	q.Set("hourly", "temperature_2m,precipitation_probability,weather_code")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,sunrise,sunset")
	if imperial {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	} else {
		q.Set("temperature_unit", "celsius")
		q.Set("wind_speed_unit", "kmh")
	}
	q.Set("timezone", tz)
	q.Set("forecast_days", fmt.Sprint(daysShown+1)) // today plus the days shown

	var fr forecastResponse
	if err := s.get(ctx, s.forecastURL, q, &fr); err != nil {
		return Weather{}, err
	}
	return shape(p, fr, imperial, s.loc)
}

func zoneOf(fr forecastResponse, def *time.Location) *time.Location {
	if fr.Timezone != "" {
		if loc, err := time.LoadLocation(fr.Timezone); err == nil {
			return loc
		}
	}
	if fr.UTCOffset != 0 {
		return time.FixedZone("", fr.UTCOffset)
	}
	return def
}

func round(f *float64) *int {
	if f == nil {
		return nil
	}
	v := int(math.Round(*f))
	return &v
}

func at[T any](s []T, i int) (v T) {
	if i >= 0 && i < len(s) {
		return s[i]
	}
	return v
}

// shape converts the API response to the card data of docs/PROTOCOL.md.
func shape(p Place, fr forecastResponse, imperial bool, def *time.Location) (Weather, error) {
	loc := zoneOf(fr, def)
	ts := func(v string) (string, error) {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, loc)
		if err != nil {
			return "", fmt.Errorf("weather service sent a bad time %q", v)
		}
		return t.Format(time.RFC3339), nil
	}
	w := Weather{Place: p.Name, Unit: "C", Hours: []Hour{}, Days: []Day{}}
	if imperial {
		w.Unit = "F"
	}
	if fr.Current.Temperature == nil || fr.Current.Code == nil {
		return Weather{}, errors.New("weather service sent no current conditions")
	}
	w.Now = Now{Temp: *round(fr.Current.Temperature), Condition: Condition(*fr.Current.Code), Code: *fr.Current.Code}
	w.Now.FeelsLike = w.Now.Temp
	if f := round(fr.Current.Apparent); f != nil {
		w.Now.FeelsLike = *f
	}

	d := fr.Daily
	if len(d.Time) == 0 || len(d.High) == 0 || len(d.Low) == 0 {
		return Weather{}, errors.New("weather service sent no daily forecast")
	}
	w.Today = Today{High: round(at(d.High, 0)), Low: round(at(d.Low, 0)), PrecipChance: round(at(d.Precip, 0))}
	var err error
	if v := at(d.Sunrise, 0); v != "" {
		if w.Today.Sunrise, err = ts(v); err != nil {
			return Weather{}, err
		}
	}
	if v := at(d.Sunset, 0); v != "" {
		if w.Today.Sunset, err = ts(v); err != nil {
			return Weather{}, err
		}
	}

	// Hours: the next 12 starting with the current hour.
	cur, err := time.ParseInLocation("2006-01-02T15:04", fr.Current.Time, loc)
	if err != nil {
		return Weather{}, fmt.Errorf("weather service sent a bad time %q", fr.Current.Time)
	}
	from := cur.Truncate(time.Hour)
	for i, v := range fr.Hourly.Time {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, loc)
		if err != nil {
			return Weather{}, fmt.Errorf("weather service sent a bad time %q", v)
		}
		if t.Before(from) {
			continue
		}
		code := at(fr.Hourly.Code, i)
		h := Hour{Time: t.Format(time.RFC3339), Temp: round(at(fr.Hourly.Temp, i)), PrecipChance: round(at(fr.Hourly.Precip, i))}
		if code != nil {
			h.Code = *code
		}
		w.Hours = append(w.Hours, h)
		if len(w.Hours) == hoursShown {
			break
		}
	}

	// Days: the five after today.
	for i := 1; i < len(d.Time) && len(w.Days) < daysShown; i++ {
		day := Day{Date: d.Time[i], High: round(at(d.High, i)), Low: round(at(d.Low, i)), PrecipChance: round(at(d.Precip, i))}
		if c := at(d.Code, i); c != nil {
			day.Code = *c
		}
		w.Days = append(w.Days, day)
	}
	return w, nil
}
