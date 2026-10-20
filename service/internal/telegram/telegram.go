// Package telegram is Hekate's backup channel. When web push reaches none of
// the owner's devices, the notification goes to their Telegram chat instead;
// approvals come with Allow and Deny buttons, and replying to one denies it with
// the reply as the reason. It's for alerts and decisions, not conversation. The
// bot long-polls the Bot API, so nothing is exposed, and it only ever talks to
// the owner's chat.
package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/loafykitten/hekate/service/internal/approvals"
	"github.com/loafykitten/hekate/service/internal/push"
	"github.com/loafykitten/hekate/service/internal/store"
)

// Limits.
const (
	pollTimeout   = 50 * time.Second // long poll; the client waits a little longer
	sendTimeout   = 15 * time.Second
	retryMin      = 5 * time.Second
	retryMax      = time.Minute
	maxText       = 3500 // Telegram allows 4096 characters
	keepApprovals = 30 * 24 * time.Hour
	offsetSetting = "telegram.offset"
	approvalTag   = "approval-"
)

// Options are the dependencies of a Service.
type Options struct {
	Token  string // the bot's token; empty means Telegram is off
	ChatID int64  // the owner's chat; nothing else is ever answered
	Store  *store.Store
	Logger *slog.Logger
	// BaseURL and HTTPClient reach the Bot API; tests replace them.
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
	// SendTimeout bounds one outgoing message; default 15 s.
	SendTimeout time.Duration
}

// Decider settles approvals; approvals.Service satisfies it.
type Decider interface {
	Decide(ctx context.Context, id int64, decision, note string) (approvals.Approval, error)
}

// Service sends to and listens on the owner's Telegram chat.
type Service struct {
	token   string
	chat    int64
	store   *store.Store
	log     *slog.Logger
	base    string
	client  *http.Client
	now     func() time.Time
	sendFor time.Duration
	decider Decider
}

// New builds the service. Set a Decider before Run for the approval buttons.
func New(o Options) *Service {
	s := &Service{token: o.Token, chat: o.ChatID, store: o.Store, log: o.Logger, base: o.BaseURL, client: o.HTTPClient, now: o.Now, sendFor: o.SendTimeout}
	if s.sendFor <= 0 {
		s.sendFor = sendTimeout
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.base == "" {
		s.base = "https://api.telegram.org"
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: pollTimeout + 15*time.Second}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Configured reports whether there is a bot and a chat to send to.
func (s *Service) Configured() bool { return s.token != "" && s.chat != 0 }

// SetDecider lets the approval buttons and replies decide approvals.
func (s *Service) SetDecider(d Decider) { s.decider = d }

// call invokes a Bot API method. Errors never carry the URL: it holds the token.
func (s *Service) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/bot"+s.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: bad request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: status %d, unreadable reply", method, resp.StatusCode)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram %s: %w", method, err)
		}
	}
	return nil
}

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type keyboard struct {
	Rows [][]button `json:"inline_keyboard"`
}

type sentMessage struct {
	MessageID int64 `json:"message_id"`
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxText {
		return s
	}
	return string([]rune(s)[:maxText-1]) + "…"
}

// Send messages the owner. With an approval id the message carries Allow and
// Deny buttons, and a reply to it denies the approval with the reply as reason.
func (s *Service) Send(ctx context.Context, text string, approvalID int64) error {
	if !s.Configured() {
		return errors.New("telegram is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, s.sendFor)
	defer cancel()
	params := map[string]any{"chat_id": s.chat, "text": clip(text)}
	if approvalID > 0 {
		id := strconv.FormatInt(approvalID, 10)
		params["reply_markup"] = keyboard{Rows: [][]button{{
			{Text: "Allow", Data: "approval:" + id + ":once"},
			{Text: "Deny", Data: "approval:" + id + ":deny"},
		}}}
		params["text"] = clip(text) + "\n\nReply to this message to deny it with a reason."
	}
	var m sentMessage
	if err := s.call(ctx, "sendMessage", params, &m); err != nil {
		return err
	}
	if approvalID > 0 {
		if _, err := s.store.DB().ExecContext(ctx, `INSERT OR REPLACE INTO telegram_approvals (message_id, approval_id, created_at) VALUES (?, ?, ?)`,
			m.MessageID, approvalID, s.now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("remember the telegram approval message: %w", err)
		}
	}
	return nil
}

// Pusher delivers web push; push.Service satisfies it, and so does a Fallback.
type Pusher interface {
	Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error)
}

// Fallback wraps a pusher: when a push reaches no device (none subscribed, or
// every send failed), the notification goes to Telegram instead. An approval's
// push (tag "approval-<id>") arrives with its buttons.
func (s *Service) Fallback(p Pusher) Pusher { return fallback{p, s} }

type fallback struct {
	push Pusher
	tg   *Service
}

func (f fallback) Send(ctx context.Context, p push.Payload, urgency push.Urgency) (push.Result, error) {
	res, err := f.push.Send(ctx, p, urgency)
	if !f.tg.Configured() || (err == nil && res.Sent > 0) {
		return res, err
	}
	var approvalID int64
	if rest, ok := strings.CutPrefix(p.Tag, approvalTag); ok {
		approvalID, _ = strconv.ParseInt(rest, 10, 64)
	}
	text := p.Title
	if p.Body != "" {
		text += "\n" + p.Body
	}
	// Sent even if the caller is giving up, but never longer than a send takes.
	if terr := f.tg.Send(context.WithoutCancel(ctx), text, approvalID); terr != nil {
		f.tg.log.Error("cannot send the telegram backup", "error", terr)
	} else {
		res.Backup = true
	}
	return res, err
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
	Callback *struct {
		ID      string   `json:"id"`
		From    user     `json:"from"`
		Message *message `json:"message"`
		Data    string   `json:"data"`
	} `json:"callback_query"`
}

type user struct {
	ID int64 `json:"id"`
}

type message struct {
	MessageID int64 `json:"message_id"`
	From      *user `json:"from"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text    string   `json:"text"`
	ReplyTo *message `json:"reply_to_message"`
}

// Run long-polls for the owner's button taps and replies until ctx is done.
func (s *Service) Run(ctx context.Context) {
	if !s.Configured() {
		return
	}
	offset, _ := s.store.GetSettingInt(ctx, offsetSetting, 0)
	wait := retryMin
	for ctx.Err() == nil {
		var updates []update
		err := s.call(ctx, "getUpdates", map[string]any{
			"offset": offset, "timeout": int(pollTimeout / time.Second),
			"allowed_updates": []string{"message", "callback_query"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("telegram poll failed", "error", err, "retry_in", wait.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, retryMax)
			continue
		}
		wait = retryMin
		for _, u := range updates {
			s.handle(ctx, u)
			offset = int(u.UpdateID) + 1
		}
		if len(updates) > 0 {
			if err := s.store.SetSettingInt(ctx, offsetSetting, offset); err != nil {
				s.log.Error("cannot save the telegram offset", "error", err)
			}
		}
		s.prune(ctx)
	}
}

// handle acts on one update from the owner's chat; anything else is ignored.
func (s *Service) handle(ctx context.Context, u update) {
	switch {
	case u.Callback != nil:
		cb := u.Callback
		if cb.From.ID != s.chat || cb.Message == nil || cb.Message.Chat.ID != s.chat {
			return
		}
		// Acknowledged at once, so the button stops spinning; the action can take
		// a while, and its outcome follows as a reply.
		actx, cancel := context.WithTimeout(ctx, s.sendFor)
		defer cancel()
		if err := s.call(actx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": "On it…"}, nil); err != nil {
			s.log.Warn("cannot answer a telegram button", "error", err)
		}
		reply, settled := s.decideButton(ctx, cb.Data)
		// Once it's decided (here or elsewhere) the buttons have done their job;
		// after a failure that might pass, they stay for another try.
		if settled {
			ectx, cancel := context.WithTimeout(ctx, s.sendFor)
			defer cancel()
			if err := s.call(ectx, "editMessageReplyMarkup", map[string]any{
				"chat_id": s.chat, "message_id": cb.Message.MessageID, "reply_markup": keyboard{Rows: [][]button{}},
			}, nil); err != nil {
				s.log.Warn("cannot clear telegram buttons", "error", err)
			}
		}
		s.say(ctx, reply, cb.Message.MessageID)
	case u.Message != nil:
		m := u.Message
		if m.Chat.ID != s.chat || m.From == nil || m.From.ID != s.chat {
			return
		}
		if m.ReplyTo != nil {
			if id, ok := s.approvalFor(ctx, m.ReplyTo.MessageID); ok {
				reply, _ := s.decide(ctx, id, approvals.DecisionDeny, m.Text)
				s.say(ctx, reply, m.MessageID)
				return
			}
		}
		s.say(ctx, "I only carry alerts and decisions here. Talk to Renamon in the app.", m.MessageID)
	}
}

// decideButton runs an "approval:<id>:<decision>" button. settled reports
// whether the approval is now decided, so its buttons can go.
func (s *Service) decideButton(ctx context.Context, data string) (reply string, settled bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "approval" || (parts[2] != approvals.DecisionOnce && parts[2] != approvals.DecisionDeny) {
		return "I don't know that button.", false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return "I don't know that button.", false
	}
	return s.decide(ctx, id, parts[2], "")
}

// decide settles an approval and says how it went; settled is false only when
// it failed in a way another try might not.
func (s *Service) decide(ctx context.Context, id int64, decision, note string) (reply string, settled bool) {
	if s.decider == nil {
		return "Approvals aren't available right now.", false
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 500 {
		note = string([]rune(note)[:500])
	}
	a, err := s.decider.Decide(ctx, id, decision, note)
	switch {
	case errors.Is(err, approvals.ErrNotPending):
		return "That one was already decided, or it expired.", true
	case err != nil:
		s.log.Error("telegram decision failed", "approval", id, "error", err)
		return "That didn't go through: " + err.Error(), false
	case a.Status == approvals.StatusDenied && note != "":
		return "Denied, with your reason.", true
	case a.Status == approvals.StatusDenied:
		return "Denied.", true
	case a.Status == approvals.StatusDone && a.Result != nil:
		return "Allowed: " + *a.Result, true
	case a.Status == approvals.StatusFailed && a.Result != nil:
		return "Allowed, but it failed: " + *a.Result, true
	default:
		return "Allowed.", true
	}
}

// say replies in the chat, quoting the message it answers.
func (s *Service) say(ctx context.Context, text string, replyTo int64) {
	ctx, cancel := context.WithTimeout(ctx, s.sendFor)
	defer cancel()
	params := map[string]any{"chat_id": s.chat, "text": clip(text)}
	if replyTo > 0 {
		params["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	if err := s.call(ctx, "sendMessage", params, nil); err != nil {
		s.log.Warn("cannot reply on telegram", "error", err)
	}
}

func (s *Service) approvalFor(ctx context.Context, messageID int64) (int64, bool) {
	var id int64
	err := s.store.DB().QueryRowContext(ctx, `SELECT approval_id FROM telegram_approvals WHERE message_id = ?`, messageID).Scan(&id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Error("cannot look up a telegram approval", "error", err)
		}
		return 0, false
	}
	return id, true
}

// prune forgets approval messages too old to matter (approvals expire long before).
func (s *Service) prune(ctx context.Context) {
	if _, err := s.store.DB().ExecContext(ctx, `DELETE FROM telegram_approvals WHERE created_at < ?`,
		s.now().Add(-keepApprovals).UTC().Format(time.RFC3339)); err != nil {
		s.log.Warn("cannot prune telegram approvals", "error", err)
	}
}
