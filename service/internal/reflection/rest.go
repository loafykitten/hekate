package reflection

import (
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/journal", s.handleJournal)
	mux.HandleFunc("POST /api/reflections/changes/{id}/undo", s.handleUndo)
}

func (s *Service) handleJournal(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	days, err := s.Journal(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"days": days})
}

func (s *Service) handleUndo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(w, s.log, r, httpx.BadRequest("change id must be a positive whole number"))
		return
	}
	c, err := s.Undo(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}
