package push

import (
	"net/http"

	"github.com/loafykitten/hekate/service/internal/httpx"
)

// Routes registers the REST endpoints from PROTOCOL.md.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/push/key", s.handleKey)
	mux.HandleFunc("POST /api/push/subscriptions", s.handleSubscribe)
	mux.HandleFunc("DELETE /api/push/subscriptions", s.handleUnsubscribe)
	mux.HandleFunc("POST /api/push/test", s.handleTest)
}

func (s *Service) handleKey(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"public_key": s.public})
}

func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var sub Subscription
	if err := httpx.Decode(w, r, &sub); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if err := s.Subscribe(r.Context(), sub); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Service) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if err := httpx.Decode(w, r, &b); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	if b.Endpoint == "" {
		httpx.Fail(w, s.log, r, httpx.BadRequest("endpoint is required"))
		return
	}
	if err := s.Unsubscribe(r.Context(), b.Endpoint); err != nil {
		httpx.Fail(w, s.log, r, err)
		return
	}
	httpx.NoContent(w)
}

// handleTest sends a test notification. It reports honestly when nothing could
// be delivered, so the app can tell the owner instead of showing a false success.
func (s *Service) handleTest(w http.ResponseWriter, r *http.Request) {
	res, err := s.Send(r.Context(), Payload{
		Title: "Hekate", Body: "Notifications are working.", URL: "/", Tag: "test",
	}, UrgencyNormal)
	switch {
	case err != nil:
		httpx.Fail(w, s.log, r, err)
	case res.Sent > 0:
		httpx.NoContent(w)
	case res.Removed+res.Failed == 0:
		httpx.Fail(w, s.log, r, httpx.Conflict("no device is subscribed to notifications"))
	default:
		httpx.Fail(w, s.log, r, &httpx.Error{Status: http.StatusBadGateway, Msg: "the push service did not accept the notification"})
	}
}
