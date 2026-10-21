package money

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md. Nothing here returns an
// access token.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/money/status", s.handleStatus)
	mux.HandleFunc("POST /api/money/link-token", s.handleLinkToken)
	mux.HandleFunc("POST /api/money/items", s.handleConnect)
	mux.HandleFunc("DELETE /api/money/items/{id}", s.handleRemove)
	mux.HandleFunc("POST /api/money/sync", s.handleSync)
	mux.HandleFunc("GET /api/money/accounts", s.handleAccounts)
	mux.HandleFunc("GET /api/money/transactions", s.handleTransactions)
	mux.HandleFunc("GET /api/money/spending", s.handleSpending)
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	items := []Item{}
	if s.Configured() {
		var err error
		if items, err = s.Items(r.Context()); err != nil {
			httpx.Fail(w, s.log, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"configured": s.Configured(), "items": items})
}

func (s *Service) handleLinkToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ItemID string `json:"item_id"`
	}
	if err := httpx.DecodeOptional(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	token, err := s.LinkToken(r.Context(), b.ItemID)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"link_token": token})
}

func (s *Service) handleConnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PublicToken string `json:"public_token"`
		Institution struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"institution"`
		Accounts []LinkedAccount `json:"accounts"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	it, err := s.Connect(r.Context(), b.PublicToken, b.Institution.ID, b.Institution.Name, b.Accounts)
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, it)
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.Remove(r.Context(), r.PathValue("id")); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Service) handleSync(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		httpx.Fail(w, s.log, r, ErrNotConfigured)
		return
	}
	s.syncSoon()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) handleAccounts(w http.ResponseWriter, r *http.Request) {
	accounts := []Account{}
	if s.Configured() {
		var err error
		if accounts, err = s.Accounts(r.Context()); err != nil {
			httpx.Fail(w, s.log, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Service) handleTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := Query{
		Text: strings.TrimSpace(q.Get("q")), Account: q.Get("account"), Category: q.Get("category"),
		From: q.Get("from"), To: q.Get("to"),
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.Fail(w, s.log, r, httpx.BadRequest("limit must be a positive whole number"))
			return
		}
		query.Limit = n
	}
	txns := []Transaction{}
	if s.Configured() {
		var err error
		if txns, err = s.Transactions(r.Context(), query); err != nil {
			httpx.Fail(w, s.log, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"transactions": txns})
}

func (s *Service) handleSpending(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		// Nothing to total yet, but the answer keeps its shape.
		httpx.JSON(w, http.StatusOK, summarize(s.now().In(s.loc).Format("2006-01"), "USD", nil, nil))
		return
	}
	sp, err := s.Spending(r.Context(), r.URL.Query().Get("month"))
	if err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sp)
}
