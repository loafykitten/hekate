package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

const (
	token = "123:secret-token"
	owner = int64(8191)
)

// fakeBot is the Bot API: it records every call and hands out queued updates.
type fakeBot struct {
	mu      sync.Mutex
	calls   []call
	updates []update
	nextID  int64
	srv     *httptest.Server
}

type call struct {
	method string
	params map[string]any
}

func newFakeBot(t *testing.T) *fakeBot {
	t.Helper()
	b := &fakeBot{nextID: 100}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ok := strings.CutPrefix(r.URL.Path, "/bot"+token+"/")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Unauthorized"}`)
			return
		}
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.calls = append(b.calls, call{method, params})
		var result any = true
		switch method {
		case "sendMessage":
			b.nextID++
			result = map[string]any{"message_id": b.nextID}
		case "getUpdates":
			result = b.updates
			b.updates = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBot) sent(method string) []call {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []call
	for _, c := range b.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

type fakeDecider struct {
	mu        sync.Mutex
	decisions []string
	err       error
}

func (d *fakeDecider) Decide(_ context.Context, id int64, decision, note string) (approvals.Approval, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.decisions = append(d.decisions, strings.Join([]string{strconv.FormatInt(id, 10), decision, note}, "|"))
	if d.err != nil {
		return approvals.Approval{}, d.err
	}
	status := approvals.StatusDone
	if decision == approvals.DecisionDeny {
		status = approvals.StatusDenied
	}
	result := "Moved 3 messages to Trash"
	return approvals.Approval{Status: status, Result: &result}, nil
}

func newService(t *testing.T, b *fakeBot) (*Service, *fakeDecider) {
	t.Helper()
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(Options{Token: token, ChatID: owner, Store: db, BaseURL: b.srv.URL, HTTPClient: b.srv.Client()})
	d := &fakeDecider{}
	s.SetDecider(d)
	return s, d
}

type fakePush struct{ res push.Result }

func (p fakePush) Send(context.Context, push.Payload, push.Urgency) (push.Result, error) {
	return p.res, nil
}

func TestFallbackOnlyWhenPushReachesNoDevice(t *testing.T) {
	b := newFakeBot(t)
	s, _ := newService(t, b)
	reached := s.Fallback(fakePush{push.Result{Sent: 1}})
	if res, err := reached.Send(t.Context(), push.Payload{Title: "Reminder", Body: "Stretch"}, push.UrgencyNormal); err != nil || res.Backup {
		t.Fatalf("%+v %v", res, err)
	}
	if n := len(b.sent("sendMessage")); n != 0 {
		t.Fatalf("telegram got %d messages although push reached a device", n)
	}
	missed := s.Fallback(fakePush{push.Result{Failed: 2}})
	if res, err := missed.Send(t.Context(), push.Payload{Title: "Reminder", Body: "Stretch", Tag: "reminder-4"}, push.UrgencyNormal); err != nil || !res.Backup {
		t.Fatalf("the backup should report it took the message: %+v %v", res, err)
	}
	sent := b.sent("sendMessage")
	if len(sent) != 1 || sent[0].params["text"] != "Reminder\nStretch" || sent[0].params["chat_id"] != float64(owner) || sent[0].params["reply_markup"] != nil {
		t.Errorf("sent = %+v", sent)
	}
}

func TestApprovalButtons(t *testing.T) {
	b := newFakeBot(t)
	s, d := newService(t, b)
	if _, err := s.Fallback(fakePush{}).Send(t.Context(), push.Payload{Title: "Approval needed", Body: "Trash 3 promos?", Tag: "approval-7"}, push.UrgencyHigh); err != nil {
		t.Fatal(err)
	}
	sent := b.sent("sendMessage")
	if len(sent) != 1 || !strings.Contains(sent[0].params["text"].(string), "Reply to this message") {
		t.Fatalf("sent = %+v", sent)
	}
	kb, _ := json.Marshal(sent[0].params["reply_markup"])
	if !strings.Contains(string(kb), `"callback_data":"approval:7:once"`) || !strings.Contains(string(kb), `"callback_data":"approval:7:deny"`) {
		t.Errorf("keyboard = %s", kb)
	}

	tap := func(from int64, data string) update {
		u := update{UpdateID: 1}
		u.Callback = &struct {
			ID      string   `json:"id"`
			From    user     `json:"from"`
			Message *message `json:"message"`
			Data    string   `json:"data"`
		}{ID: "cb", From: user{ID: from}, Data: data, Message: &message{MessageID: 101}}
		u.Callback.Message.Chat.ID = from
		return u
	}
	s.handle(t.Context(), tap(999, "approval:7:once")) // someone else's chat
	if len(d.decisions) != 0 {
		t.Fatalf("a stranger decided: %v", d.decisions)
	}
	s.handle(t.Context(), tap(owner, "approval:7:once"))
	if len(d.decisions) != 1 || d.decisions[0] != "7|once|" {
		t.Errorf("decisions = %v", d.decisions)
	}
	answers := b.sent("answerCallbackQuery")
	if len(answers) != 1 || answers[0].params["text"] != "On it…" {
		t.Errorf("answers = %+v", answers)
	}
	if sent := b.sent("sendMessage"); sent[len(sent)-1].params["text"] != "Allowed: Moved 3 messages to Trash" {
		t.Errorf("outcome = %+v", sent[len(sent)-1])
	}
	if len(b.sent("editMessageReplyMarkup")) != 1 {
		t.Error("the buttons were left on the message")
	}
	s.handle(t.Context(), tap(owner, "approval:x:always"))
	if len(d.decisions) != 1 {
		t.Errorf("a malformed button decided something: %v", d.decisions)
	}
}

func TestReplyDeniesWithReason(t *testing.T) {
	b := newFakeBot(t)
	s, d := newService(t, b)
	if err := s.Send(t.Context(), "Approval needed\nTrash 3 promos?", 7); err != nil {
		t.Fatal(err)
	}
	msgID := int64(101) // the fake's first message id
	reply := func(from int64, to int64, text string) update {
		m := &message{MessageID: 500, From: &user{ID: from}, Text: text, ReplyTo: &message{MessageID: to}}
		m.Chat.ID = from
		return update{UpdateID: 2, Message: m}
	}
	s.handle(t.Context(), reply(owner, msgID, "  I still read those.  "))
	if len(d.decisions) != 1 || d.decisions[0] != "7|deny|I still read those." {
		t.Errorf("decisions = %v", d.decisions)
	}
	s.handle(t.Context(), reply(999, msgID, "deny it"))
	if len(d.decisions) != 1 {
		t.Errorf("a stranger's reply decided: %v", d.decisions)
	}
	// A message that isn't a reply to an approval gets a pointer to the app.
	s.handle(t.Context(), reply(owner, 12345, "hi"))
	last := b.sent("sendMessage")
	if !strings.Contains(last[len(last)-1].params["text"].(string), "Talk to Renamon in the app") {
		t.Errorf("last = %+v", last[len(last)-1])
	}
	d.err = approvals.ErrNotPending
	if got, settled := s.decide(t.Context(), 7, approvals.DecisionDeny, ""); !strings.Contains(got, "already decided") || !settled {
		t.Errorf("not pending = %q", got)
	}
}

func TestRunPollsAndKeepsTheOffset(t *testing.T) {
	b := newFakeBot(t)
	s, d := newService(t, b)
	if err := s.Send(t.Context(), "Approval needed", 7); err != nil {
		t.Fatal(err)
	}
	m := &message{MessageID: 500, From: &user{ID: owner}, Text: "not now", ReplyTo: &message{MessageID: 101}}
	m.Chat.ID = owner
	b.mu.Lock()
	b.updates = []update{{UpdateID: 41, Message: m}}
	b.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	// Wait for the poll after the update, which must ask from past it.
	polledPast := func() bool {
		for _, c := range b.sent("getUpdates") {
			if c.params["offset"] == float64(42) {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(5 * time.Second)
	for !polledPast() {
		if time.Now().After(deadline) {
			t.Fatal("no poll asked from past the update")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if len(d.decisions) != 1 || d.decisions[0] != "7|deny|not now" {
		t.Errorf("decisions = %v", d.decisions)
	}
	if off, _ := s.store.GetSettingInt(t.Context(), offsetSetting, 0); off != 42 {
		t.Errorf("saved offset = %d, want 42", off)
	}
}

func TestErrorsNeverCarryTheToken(t *testing.T) {
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(Options{Token: token, ChatID: owner, Store: db, BaseURL: "http://127.0.0.1:1"})
	err = s.Send(t.Context(), "hi", 0)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("err = %v", err)
	}
	off := New(Options{Store: db})
	if off.Configured() || off.Send(t.Context(), "hi", 0) == nil {
		t.Error("an unconfigured bot sent")
	}
	// Unconfigured, the fallback is just the pusher.
	if _, err := off.Fallback(fakePush{}).Send(t.Context(), push.Payload{Title: "x"}, push.UrgencyNormal); err != nil {
		t.Error(err)
	}
}

func TestSendsDontHangOnAStalledTelegram(t *testing.T) {
	stall := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(stall); srv.Close() })
	db, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(Options{Token: token, ChatID: owner, Store: db, BaseURL: srv.URL, HTTPClient: srv.Client(), SendTimeout: 200 * time.Millisecond})
	start := time.Now()
	if _, err := s.Fallback(fakePush{}).Send(t.Context(), push.Payload{Title: "Reminder"}, push.UrgencyNormal); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the backup send blocked for %s", took)
	}
}

func TestAFailedDecisionKeepsTheButtons(t *testing.T) {
	b := newFakeBot(t)
	s, d := newService(t, b)
	d.err = errors.New("database is locked")
	u := update{UpdateID: 1}
	u.Callback = &struct {
		ID      string   `json:"id"`
		From    user     `json:"from"`
		Message *message `json:"message"`
		Data    string   `json:"data"`
	}{ID: "cb", From: user{ID: owner}, Data: "approval:7:once", Message: &message{MessageID: 101}}
	u.Callback.Message.Chat.ID = owner
	s.handle(t.Context(), u)
	if n := len(b.sent("editMessageReplyMarkup")); n != 0 {
		t.Errorf("a failed decision took the buttons away (%d edits)", n)
	}
}
