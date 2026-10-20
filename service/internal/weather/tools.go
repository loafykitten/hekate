package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/agent"
)

const noHome = "The user's home place is not set, so I don't know where they live. Ask them which city they live in, " +
	"then call set_home_place. Or pass a place to this tool."

// Tools returns the weather and set_home_place tools.
func (s *Service) Tools() []agent.Tool {
	return []agent.Tool{s.weatherTool(), s.setHomeTool()}
}

func (s *Service) weatherTool() agent.Tool {
	return agent.Tool{
		Name: "weather",
		Description: "Current weather and forecast (next 12 hours and next 5 days) for a place. Leave place empty for the " +
			"user's home. Use it for any question about the weather, rain, temperature or what to wear.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"place":{"type":"string","description":"A city such as 'Austin, Texas' or 'Paris'. Default: the user's home."}}}`),
		Effect:  agent.EffectRead,
		Label:   "Checking the weather",
		Timeout: 20 * time.Second,
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Place string `json:"place"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return agent.Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			w, err := s.Forecast(ctx, a.Place)
			switch {
			case errors.Is(err, ErrNoHome):
				return agent.Result{Content: noHome}, nil
			case errors.Is(err, errNoPlace), errors.Is(err, errBadPlace):
				return agent.Result{}, err
			case err != nil:
				s.log.Warn("weather lookup failed", "err", err)
				return agent.Result{}, errors.New("the weather service could not be reached or gave an unexpected answer")
			}
			data, err := json.Marshal(w)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{
				Content: Describe(w),
				Card:    &agent.Card{Kind: "weather", ID: "weather", Title: "Weather: " + w.Place, Data: data},
			}, nil
		},
	}
}

func (s *Service) setHomeTool() agent.Tool {
	return agent.Tool{
		Name: "set_home_place",
		Description: "Remember where the user lives, for weather and local questions. Give a city, with the state or country " +
			"when it could be ambiguous ('Austin, Texas'). It returns the place it resolved to: tell the user and confirm it is right.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"place":{"type":"string","description":"City, optionally with state or country."}},
			"required":["place"]}`),
		Effect:  agent.EffectLocal,
		Label:   "Saving your home place",
		Timeout: 20 * time.Second,
		Run: func(ctx context.Context, raw json.RawMessage) (agent.Result, error) {
			var a struct {
				Place string `json:"place"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return agent.Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			p, others, err := s.SetHomePlace(ctx, a.Place)
			if err != nil {
				if errors.Is(err, errNoPlace) || errors.Is(err, errBadPlace) {
					return agent.Result{}, err
				}
				s.log.Warn("set home place failed", "err", err)
				return agent.Result{}, errors.New("could not look that place up or save it")
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Home place saved as %s (%.2f, %.2f, time zone %s).", p.Name, p.Latitude, p.Longitude, p.Timezone)
			if len(others) > 0 {
				sb.WriteString(" Other matches:")
				for i, o := range others {
					if i == 3 {
						break
					}
					sb.WriteString(" " + o.Name + ";")
				}
			}
			sb.WriteString(" Tell the user which place it is and check that it is right; if not, call set_home_place again with a more specific name.")
			return agent.Result{Content: sb.String()}, nil
		},
	}
}

// PromptContext returns one line about the home weather for the system
// prompt, or "" when no home place is set. It relies on the 10 minute forecast
// cache, so calling it every turn is cheap.
func (s *Service) PromptContext(ctx context.Context, _ time.Time) (string, error) {
	p, err := s.HomePlace(ctx)
	if errors.Is(err, ErrNoHome) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	w, err := s.forecastFor(ctx, p)
	if err != nil {
		return "", err
	}
	line := fmt.Sprintf("Weather in %s: %s, %s now (feels like %s)", w.Place, strings.ToLower(w.Now.Condition), deg(w.Now.Temp, w.Unit), deg(w.Now.FeelsLike, w.Unit))
	if w.Today.High != nil && w.Today.Low != nil {
		line += fmt.Sprintf("; today high %s, low %s", deg(*w.Today.High, w.Unit), deg(*w.Today.Low, w.Unit))
	}
	if w.Today.PrecipChance != nil {
		line += fmt.Sprintf(", %d%% chance of precipitation", *w.Today.PrecipChance)
	}
	return line + ".", nil
}

func deg(v int, unit string) string { return fmt.Sprintf("%d°%s", v, unit) }

func opt(v *int, suffix string) string {
	if v == nil {
		return "?"
	}
	return fmt.Sprintf("%d%s", *v, suffix)
}

// Describe renders the forecast for the model.
func Describe(w Weather) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Weather for %s (°%s). Now: %s, feels like %s, %s.\n", w.Place, w.Unit, deg(w.Now.Temp, w.Unit), deg(w.Now.FeelsLike, w.Unit), w.Now.Condition)
	fmt.Fprintf(&sb, "Today: high %s, low %s, precipitation chance %s", opt(w.Today.High, "°"), opt(w.Today.Low, "°"), opt(w.Today.PrecipChance, "%"))
	if sr, ok := clock(w.Today.Sunrise); ok {
		fmt.Fprintf(&sb, "; sunrise %s", sr)
	}
	if ss, ok := clock(w.Today.Sunset); ok {
		fmt.Fprintf(&sb, ", sunset %s", ss)
	}
	sb.WriteString(".\nNext hours:")
	for _, h := range w.Hours {
		c, _ := clock(h.Time)
		fmt.Fprintf(&sb, " %s %s %s %s;", c, opt(h.Temp, "°"), opt(h.PrecipChance, "%"), Condition(h.Code))
	}
	sb.WriteString("\nNext days:")
	for _, d := range w.Days {
		label := d.Date
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			label = t.Format("Mon Jan 2")
		}
		fmt.Fprintf(&sb, " %s %s/%s %s %s;", label, opt(d.High, "°"), opt(d.Low, "°"), opt(d.PrecipChance, "%"), Condition(d.Code))
	}
	return sb.String()
}

// clock formats the wall-clock time of an RFC 3339 value, in its own offset.
func clock(v string) (string, bool) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return "", false
	}
	return t.Format("15:04"), true
}
