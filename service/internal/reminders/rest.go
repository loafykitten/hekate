package reminders

import (
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/reminders", s.handleList)
	mux.HandleFunc("POST /api/reminders", s.handleCreate)
	mux.HandleFunc("PATCH /api/reminders/{id}", s.handlePatch)
	mux.HandleFunc("POST /api/reminders/{id}/snooze", s.handleSnooze)
	mux.HandleFunc("POST /api/reminders/{id}/done", s.handleDone)
	mux.HandleFunc("DELETE /api/reminders/{id}", s.handleCancel)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("reminder id must be a positive whole number")
	}
	return id, nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"reminders": list})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text  string `json:"text"`
		At    string `json:"at"`
		RRule string `json:"rrule"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	rem, err := s.Create(r.Context(), b.Text, b.At, b.RRule)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, rem)
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b struct {
		Text  *string           `json:"text"`
		At    *string           `json:"at"`
		RRule httpx.Opt[string] `json:"rrule"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	rem, err := s.Update(r.Context(), id, Patch{Text: b.Text, At: b.At, RRule: b.RRule.Ptr()})
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rem)
}

func (s *Service) handleSnooze(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b struct {
		Minutes int `json:"minutes"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	rem, err := s.Snooze(r.Context(), id, b.Minutes)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rem)
}

func (s *Service) handleDone(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	rem, err := s.Done(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rem)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if _, err := s.Cancel(r.Context(), id); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}
