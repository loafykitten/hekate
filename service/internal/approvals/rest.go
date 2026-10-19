package approvals

import (
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/approvals", s.handleList)
	mux.HandleFunc("POST /api/approvals/{id}/decide", s.handleDecide)
	mux.HandleFunc("GET /api/approvals/rules", s.handleRules)
	mux.HandleFunc("DELETE /api/approvals/rules/{id}", s.handleDeleteRule)
}

func pathID(r *http.Request, what string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("%s id must be a positive whole number", what)
	}
	return id, nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"approvals": list})
}

func (s *Service) handleDecide(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "approval")
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	a, err := s.Decide(r.Context(), id, b.Decision, b.Note)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func (s *Service) handleRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.Rules(r.Context())
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Service) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "rule")
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if err := s.DeleteRule(r.Context(), id); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}
