package tasks

import (
	"context"
	"net/http"
	"strconv"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks", s.handleList)
	mux.HandleFunc("POST /api/tasks", s.handleCreate)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.handlePatch)
	mux.HandleFunc("POST /api/tasks/{id}/complete", s.handleComplete)
	mux.HandleFunc("POST /api/tasks/{id}/reopen", s.handleReopen)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDelete)
	mux.HandleFunc("GET /api/lists", s.handleLists)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("task id must be a positive whole number")
	}
	return id, nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := Status(q.Get("status"))
	switch status {
	case "", StatusOpen, StatusDone, StatusAll:
	default:
		httpx.Fail(w, s.log, r, httpx.BadRequest("status must be open, done or all"))
		return
	}
	tasks, err := s.List(r.Context(), Filter{List: q.Get("list"), Status: status})
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

type createBody struct {
	Title    string `json:"title"`
	Notes    string `json:"notes"`
	List     string `json:"list"`
	Due      string `json:"due"`
	RRule    string `json:"rrule"`
	Priority int    `json:"priority"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	t, err := s.Create(r.Context(), NewTask(b))
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, t)
}

// patchBody accepts the Task fields. Read-only ones (id, timestamps) are
// tolerated so a client can send back an edited copy of what it fetched.
type patchBody struct {
	Title       *string           `json:"title"`
	Notes       *string           `json:"notes"`
	List        *string           `json:"list"`
	Due         httpx.Opt[string] `json:"due"`
	RRule       httpx.Opt[string] `json:"rrule"`
	Priority    *int              `json:"priority"`
	CompletedAt httpx.Opt[string] `json:"completed_at"`
	ID          httpx.Opt[string] `json:"id"`
	CreatedAt   httpx.Opt[string] `json:"created_at"`
	UpdatedAt   httpx.Opt[string] `json:"updated_at"`
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	var b patchBody
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	p := Patch{Title: b.Title, Notes: b.Notes, List: b.List, Due: b.Due.Ptr(), RRule: b.RRule.Ptr(), Priority: b.Priority}
	if b.CompletedAt.Set {
		done := b.CompletedAt.Val != nil
		p.Done = &done
	}
	t, err := s.Update(r.Context(), id, p)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (s *Service) handleComplete(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.Complete)
}

func (s *Service) handleReopen(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.Reopen)
}

func (s *Service) transition(w http.ResponseWriter, r *http.Request, f func(ctx context.Context, id int64) (Task, error)) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	t, err := f(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if _, err := s.Delete(r.Context(), id); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Service) handleLists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.Lists(r.Context())
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"lists": lists})
}
