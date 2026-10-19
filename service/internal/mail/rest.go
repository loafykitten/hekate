package mail

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md. The owner's own
// actions through REST just happen; only the model's tools ask first.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/status", s.handleStatus)
	mux.HandleFunc("GET /api/mail/summary", s.handleSummary)
	mux.HandleFunc("GET /api/mail/messages", s.handleList)
	mux.HandleFunc("GET /api/mail/messages/{id}", s.handleRead)
	mux.HandleFunc("POST /api/mail/messages/{id}/archive", s.handleArchive)
	mux.HandleFunc("POST /api/mail/messages/{id}/trash", s.handleTrash)
	mux.HandleFunc("POST /api/mail/messages/{id}/read", s.handleMarkRead)
	mux.HandleFunc("GET /api/mail/rules", s.handleRules)
	mux.HandleFunc("DELETE /api/mail/rules/{id}", s.handleDeleteRule)
}

func pathID(r *http.Request) (int64, error) { return parseID(r.PathValue("id")) }

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	accts, err := s.Status(r.Context())
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"configured": s.Configured(), "accounts": accts})
}

func (s *Service) handleSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.Summary(r.Context(), 10)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sum)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := Query{Text: strings.TrimSpace(q.Get("q")), Category: q.Get("category"), Account: q.Get("account")}
	switch q.Get("folder") {
	case "", "inbox":
	case "all":
		query.AllFolders = true
	default:
		httpx.Fail(w, s.log, r, httpx.BadRequest("folder must be inbox or all"))
		return
	}
	if v := q.Get("unread"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			httpx.Fail(w, s.log, r, httpx.BadRequest("unread must be true or false"))
			return
		}
		query.UnreadOnly = b
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.Fail(w, s.log, r, httpx.BadRequest("limit must be a positive whole number"))
			return
		}
		query.Limit = n
	}
	msgs, err := s.Messages(r.Context(), query)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Service) handleRead(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	msg, body, err := s.ReadBody(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"message": msg, "body": body})
}

// act runs a one-message action and answers with the message as it is after.
func (s *Service) act(w http.ResponseWriter, r *http.Request, do func(id int64) (Outcome, error)) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	out, err := do(id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if out.Done == 0 {
		httpx.Fail(w, s.log, r, ErrGone)
		return
	}
	s.changed()
	msg, err := s.Message(r.Context(), id)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, msg)
}

func (s *Service) handleArchive(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, func(id int64) (Outcome, error) { return s.Archive(r.Context(), []int64{id}) })
}

func (s *Service) handleTrash(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, func(id int64) (Outcome, error) { return s.Trash(r.Context(), []int64{id}) })
}

func (s *Service) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Unread *bool `json:"unread"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if b.Unread == nil {
		httpx.Fail(w, s.log, r, httpx.BadRequest("unread (true or false) is required"))
		return
	}
	s.act(w, r, func(id int64) (Outcome, error) {
		if *b.Unread {
			return s.MarkUnread(r.Context(), []int64{id})
		}
		return s.MarkRead(r.Context(), []int64{id})
	})
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
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(w, s.log, r, httpx.BadRequest("rule id must be a positive whole number"))
		return
	}
	if err := s.DeleteRule(r.Context(), id); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}
