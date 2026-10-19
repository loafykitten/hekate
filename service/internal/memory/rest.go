package memory

import (
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory", s.handleList)
	mux.HandleFunc("PATCH /api/memory/{id}", s.handlePatch)
	mux.HandleFunc("DELETE /api/memory/{id}", s.handleDelete)
	mux.HandleFunc("GET /api/memory/core", s.handleCore)
	mux.HandleFunc("PUT /api/memory/core/{key}", s.handleCoreSet)
	mux.HandleFunc("DELETE /api/memory/core/{key}", s.handleCoreRemove)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("memory id must be a positive whole number")
	}
	return id, nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			httpx.Fail(w, s.log, r, httpx.BadRequest("limit must be between 1 and %d", maxLimit))
			return
		}
		limit = n
	}
	var (
		list []Memory
		err  error
	)
	if text := q.Get("q"); text != "" {
		list, err = s.Search(r.Context(), text, q.Get("kind"), limit)
	} else {
		list, err = s.List(r.Context(), q.Get("kind"), limit)
	}
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"memories": list})
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b struct {
		Content    *string           `json:"content"`
		Kind       *string           `json:"kind"`
		Importance *int              `json:"importance"`
		FollowUpAt httpx.Opt[string] `json:"follow_up_at"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	m, err := s.Update(r.Context(), id, Patch{Content: b.Content, Kind: b.Kind, Importance: b.Importance, FollowUpAt: b.FollowUpAt.Ptr()})
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if _, err := s.Forget(r.Context(), id); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Service) handleCore(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Profile(r.Context())
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Service) handleCoreSet(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Value string `json:"value"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	e, err := s.SetProfile(r.Context(), r.PathValue("key"), b.Value)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (s *Service) handleCoreRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.RemoveProfile(r.Context(), r.PathValue("key")); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}
