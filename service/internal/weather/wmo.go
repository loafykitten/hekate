package weather

// conditions maps WMO weather interpretation codes (as used by Open-Meteo) to
// short human descriptions.
var conditions = map[int]string{
	0:  "Clear",
	1:  "Mostly clear",
	2:  "Partly cloudy",
	3:  "Overcast",
	45: "Fog",
	48: "Freezing fog",
	51: "Light drizzle",
	53: "Drizzle",
	55: "Heavy drizzle",
	56: "Light freezing drizzle",
	57: "Freezing drizzle",
	61: "Light rain",
	63: "Rain",
	65: "Heavy rain",
	66: "Light freezing rain",
	67: "Freezing rain",
	71: "Light snow",
	73: "Snow",
	75: "Heavy snow",
	77: "Snow grains",
	80: "Light rain showers",
	81: "Rain showers",
	82: "Heavy rain showers",
	85: "Light snow showers",
	86: "Heavy snow showers",
	95: "Thunderstorm",
	96: "Thunderstorm with hail",
	99: "Thunderstorm with heavy hail",
}

// Condition returns the human description of a WMO code.
func Condition(code int) string {
	if c, ok := conditions[code]; ok {
		return c
	}
	return "Unknown"
}
