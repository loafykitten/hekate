package threads

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/loafykitten/hekate/service/internal/httpx"
	"github.com/loafykitten/hekate/service/internal/proto"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/threads", s.handleList)
	mux.HandleFunc("POST /api/threads", s.handleCreate)
	mux.HandleFunc("GET /api/threads/{id}", s.handleGet)
	mux.HandleFunc("PATCH /api/threads/{id}", s.handlePatch)
	mux.HandleFunc("GET /api/threads/{id}/messages", s.handleMessages)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	archived := r.URL.Query().Get("archived") == "true"
	list, err := s.List(r.Context(), archived)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"threads": list})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Model string `json:"model"`
	}
	if err := httpx.DecodeOptional(w, r, &body); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	t, err := s.Create(r.Context(), body.Title, body.Model, OriginOwner)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, t)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	t, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if err := httpx.Decode(w, r, &p); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	t, err := s.Update(r.Context(), r.PathValue("id"), p)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

// Step is one tool call in a thread turn, for the folded steps under a reply.
type Step struct {
	TurnID    string `json:"turn_id"`
	Tool      string `json:"tool"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	CreatedAt string `json:"created_at"`
}

func (s *Service) handleMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httpx.Fail(w, s.log, r, httpx.BadRequest("before must be a message id"))
			return
		}
		before = n
	}
	limit := defaultHistory
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxHistory {
			httpx.Fail(w, s.log, r, httpx.BadRequest("limit must be between 1 and %d", maxHistory))
			return
		}
		limit = n
	}
	msgs, steps, err := s.Messages(r.Context(), id, before, limit)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	reviews, err := s.reviews(r.Context(), id, msgs)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"messages": msgs, "steps": steps, "reviews": reviews})
}

// Messages returns a page of a thread's visible history, newest last, and the
// tool steps of the turns on that page, oldest first.
func (s *Service) Messages(ctx context.Context, id string, before int64, limit int) ([]proto.HistoryMessage, []Step, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, nil, err
	}
	stored, err := s.store.VisibleMessages(ctx, id, before, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("thread messages: %w", err)
	}
	msgs := make([]proto.HistoryMessage, len(stored))
	turns := map[string]bool{}
	var turnIDs []any
	for i, m := range stored {
		msgs[i] = proto.HistoryMessage{ID: strconv.FormatInt(m.ID, 10), Role: m.Role, Text: m.Text, TurnID: m.TurnID,
			CreatedAt: s.display(m.CreatedAt.UTC().Format(time.RFC3339))}
		if !turns[m.TurnID] {
			turns[m.TurnID] = true
			turnIDs = append(turnIDs, m.TurnID)
		}
	}
	steps := []Step{}
	if len(turnIDs) == 0 {
		return msgs, steps, nil
	}
	marks := strings.Repeat(", ?", len(turnIDs)-1)
	query := `SELECT turn_id, tool, status, result, created_at FROM activity WHERE turn_id IN (?` + marks + `) ORDER BY id` //nolint:gosec // only placeholders are joined; every value is bound
	rows, err := s.db.QueryContext(ctx, query, turnIDs...)
	if err != nil {
		return nil, nil, fmt.Errorf("thread steps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st Step
		var at string
		if err := rows.Scan(&st.TurnID, &st.Tool, &st.Status, &st.Summary, &at); err != nil {
			return nil, nil, fmt.Errorf("thread steps: %w", err)
		}
		st.Label = s.labels[st.Tool]
		if st.Label == "" {
			st.Label = "Working on it"
		}
		st.CreatedAt = s.display(at)
		steps = append(steps, st)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("thread steps: %w", err)
	}
	return msgs, steps, nil
}

// reviews returns the reviews of the turns on a page of messages, or that started them.
func (s *Service) reviews(ctx context.Context, id string, msgs []proto.HistoryMessage) ([]proto.Review, error) {
	if s.opts.Reviewer == nil {
		return []proto.Review{}, nil
	}
	var turns []string
	seen := map[string]bool{}
	for _, m := range msgs {
		if !seen[m.TurnID] {
			seen[m.TurnID] = true
			turns = append(turns, m.TurnID)
		}
	}
	out, err := s.opts.Reviewer.ForTurns(ctx, id, turns)
	if err != nil {
		return nil, fmt.Errorf("thread reviews: %w", err)
	}
	return out, nil
}
