package inspect

import (
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/jobs"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ideas", s.handleIdeas)
	mux.HandleFunc("PATCH /api/ideas/{id}", s.handlePatchIdea)
	mux.HandleFunc("DELETE /api/ideas/{id}", s.handleDeleteIdea)
	mux.HandleFunc("GET /api/routines/runs", s.handleRuns)
}

func ideaID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("idea id must be a positive whole number")
	}
	return id, nil
}

func (s *Service) handleIdeas(w http.ResponseWriter, r *http.Request) {
	var all bool
	switch r.URL.Query().Get("status") {
	case "", "open":
	case "all":
		all = true
	default:
		httpx.Fail(w, s.log, r, httpx.BadRequest("status must be open or all"))
		return
	}
	list, err := s.Ideas(r.Context(), all)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ideas": list})
}

func (s *Service) handlePatchIdea(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := httpx.Decode(w, r, &body); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	idea, err := s.SetIdeaStatus(r.Context(), id, body.Status)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, idea)
}

func (s *Service) handleDeleteIdea(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if err := s.DeleteIdea(r.Context(), id); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Service) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs := []jobs.Run{}
	if s.jobs != nil {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := s.jobs.Runs(r.Context(), r.URL.Query().Get("routine"), limit)
		if err != nil {
			httpx.Fail(w, s.log, r, err)
			return
		}
		if list != nil {
			runs = list
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"runs": runs})
}
