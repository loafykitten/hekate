package changes

import (
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/changes", s.handleList)
	mux.HandleFunc("GET /api/changes/{id}", s.handleGet)
	mux.HandleFunc("POST /api/changes/{id}/claim", s.handleClaim)
	mux.HandleFunc("POST /api/changes/{id}/progress", s.handleProgress)
	mux.HandleFunc("POST /api/changes/{id}/finish", s.handleFinish)
	mux.HandleFunc("POST /api/changes/{id}/retry", s.handleRetry)
}

func (s *Service) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	c, err := s.Retry(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("change request id must be a positive whole number")
	}
	return id, nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"changes": list})
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	c, err := s.Get(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (s *Service) handleClaim(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	c, err := s.Claim(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (s *Service) handleProgress(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b struct {
		Stage    string `json:"stage"`
		IssueURL string `json:"issue_url"`
		PRURL    string `json:"pr_url"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	c, err := s.Progress(r.Context(), id, b.Stage, b.IssueURL, b.PRURL)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (s *Service) handleFinish(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b struct {
		Outcome  string `json:"outcome"`
		Result   string `json:"result"`
		IssueURL string `json:"issue_url"`
		PRURL    string `json:"pr_url"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	c, err := s.Finish(r.Context(), id, b.Outcome, b.Result, b.IssueURL, b.PRURL)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}
