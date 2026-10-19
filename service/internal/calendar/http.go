package calendar

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxBody = 64 << 10

// Register adds the calendar routes to mux:
//
//	GET    /api/calendar/status
//	GET    /api/calendar/events?from=&to=
//	POST   /api/calendar/events
//	PATCH  /api/calendar/events/{uid}
//	DELETE /api/calendar/events/{uid}
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/calendar/status", s.handleStatus)
	mux.HandleFunc("GET /api/calendar/events", s.handleList)
	mux.HandleFunc("POST /api/calendar/events", s.handleCreate)
	mux.HandleFunc("PATCH /api/calendar/events/{uid}", s.handlePatch)
	mux.HandleFunc("DELETE /api/calendar/events/{uid}", s.handleDelete)
}

func (s *Service) handleStatus(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"configured": s.Configured()}
	if s.Configured() {
		out["calendar_name"] = s.name
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigured(w) {
		return
	}
	from, to, err := s.resolveRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		s.fail(w, err)
		return
	}
	events, err := s.Events(r.Context(), from, to)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigured(w) {
		return
	}
	var in EventInput
	if !decodeBody(w, r, &in) {
		return
	}
	ev, err := s.Create(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ev)
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigured(w) {
		return
	}
	var p EventPatch
	if !decodeBody(w, r, &p) {
		return
	}
	ev, err := s.Update(r.Context(), r.PathValue("uid"), p, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigured(w) {
		return
	}
	if err := s.Delete(r.Context(), r.PathValue("uid"), false); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) requireConfigured(w http.ResponseWriter) bool {
	if s.Configured() {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, ErrNotConfigured.Error())
	return false
}

// fail maps a Service error to a status code and message.
func (s *Service) fail(w http.ResponseWriter, err error) {
	var in *InputError
	switch {
	case errors.As(err, &in):
		writeError(w, http.StatusBadRequest, in.Msg)
	case errors.Is(err, ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "no such event")
	case errors.Is(err, errConflict):
		writeError(w, http.StatusConflict, "the event was changed elsewhere; reload and try again")
	case errors.Is(err, ErrNotOwned):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, errAuth):
		s.log.Warn("calendar server rejected the credentials")
		writeError(w, http.StatusBadGateway, "the calendar server rejected Hekate's credentials")
	default:
		s.log.Warn("calendar request failed", "err", err)
		writeError(w, http.StatusBadGateway, "the calendar server could not be reached or gave an unexpected answer")
	}
}

// decodeBody reads a size-limited JSON body, rejecting unknown fields.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		}
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: unexpected data after the object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
