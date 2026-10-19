package weather

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Register adds GET /api/weather?place= to mux. Without place it reports the
// home place; with neither it answers 404.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/weather", s.handleWeather)
}

func (s *Service) handleWeather(w http.ResponseWriter, r *http.Request) {
	wx, err := s.Forecast(r.Context(), r.URL.Query().Get("place"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, wx)
	case errors.Is(err, ErrNoHome):
		writeError(w, http.StatusNotFound, "no place is known: set a home place or pass ?place=")
	case errors.Is(err, errNoPlace):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errBadPlace):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.Warn("weather request failed", "err", err)
		writeError(w, http.StatusBadGateway, "the weather service could not be reached or gave an unexpected answer")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
