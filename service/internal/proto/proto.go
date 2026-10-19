// Package proto holds the server-to-client WebSocket messages from
// docs/PROTOCOL.md. Every constructor sets the "type" field so callers cannot
// forget it.
package proto

import (
	"encoding/json"

	"github.com/loafykitten/hekate/service/internal/perform"
)

// Message types.
const (
	TypeReady        = "ready"
	TypeCapabilities = "capabilities"
	TypeTranscript   = "transcript"
	TypeTurnStart    = "turn_start"
	TypeActivity     = "activity"
	TypeSegment      = "segment"
	TypeCard         = "card"
	TypeTurnEnd      = "turn_end"
	TypeError        = "error"
)

// Error codes the service sends.
const (
	CodeLLMUnavailable = "llm_unavailable" // no key, bad key, rate limit or provider down
	CodeLLMError       = "llm_error"       // the provider rejected the request or broke protocol
	CodeEmptyReply     = "empty_reply"     // the model produced nothing to say
	CodeTimeout        = "turn_timeout"    // the turn hit its deadline
	CodeSTTUnavailable = "stt_unavailable"
	CodeSTTFailed      = "stt_failed"
	CodeBadMessage     = "bad_message"
	CodeTooLong        = "too_long"
)

// Turn end statuses.
const (
	StatusDone        = "done"
	StatusInterrupted = "interrupted"
	StatusError       = "error"
)

// Capabilities says which voice features work right now.
type Capabilities struct {
	STT bool `json:"stt"`
	TTS bool `json:"tts"`
}

// Ready is sent once on connect.
type Ready struct {
	Type           string       `json:"type"`
	ClientID       string       `json:"client_id"`
	ConversationID string       `json:"conversation_id"`
	Capabilities   Capabilities `json:"capabilities"`
	Gestures       []string     `json:"gestures"`
}

// CapabilitiesChanged tells clients that what the service can do changed after
// they connected, e.g. speech-to-text finished loading.
type CapabilitiesChanged struct {
	Type         string       `json:"type"`
	Capabilities Capabilities `json:"capabilities"`
}

// NewCapabilities builds a capabilities message.
func NewCapabilities(c Capabilities) CapabilitiesChanged {
	return CapabilitiesChanged{TypeCapabilities, c}
}

// NewReady builds a ready message; a nil gesture list is sent as [].
func NewReady(clientID, conversationID string, caps Capabilities, gestures []string) Ready {
	if gestures == nil {
		gestures = []string{}
	}
	return Ready{TypeReady, clientID, conversationID, caps, gestures}
}

// Transcript is the push-to-talk result for input ID.
type Transcript struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Text string `json:"text"`
}

// NewTranscript builds a transcript message.
func NewTranscript(id, text string) Transcript { return Transcript{TypeTranscript, id, text} }

// TurnStart announces that she is answering UserText.
type TurnStart struct {
	Type     string `json:"type"`
	TurnID   string `json:"turn_id"`
	InputID  string `json:"input_id"`
	Origin   string `json:"origin"`
	UserText string `json:"user_text"`
}

// NewTurnStart builds a turn_start message.
func NewTurnStart(turnID, inputID, origin, userText string) TurnStart {
	return TurnStart{TypeTurnStart, turnID, inputID, origin, userText}
}

// Activity reports a tool starting, finishing or failing.
type Activity struct {
	Type   string `json:"type"`
	TurnID string `json:"turn_id"`
	Tool   string `json:"tool"`
	Label  string `json:"label"`
	State  string `json:"state"` // start, done, error
}

// Activity states.
const (
	ActivityStart = "start"
	ActivityDone  = "done"
	ActivityError = "error"
)

// NewActivity builds an activity message.
func NewActivity(turnID, tool, label, state string) Activity {
	return Activity{TypeActivity, turnID, tool, label, state}
}

// Segment is one sentence of her reply, ready to perform.
type Segment struct {
	Type   string `json:"type"`
	TurnID string `json:"turn_id"`
	perform.Segment
}

// NewSegment builds a segment message.
func NewSegment(turnID string, s perform.Segment) Segment { return Segment{TypeSegment, turnID, s} }

// Card is something to show on screen.
type Card struct {
	Kind  string          `json:"kind"`
	ID    string          `json:"id"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}

// CardMsg wraps a card for a turn.
type CardMsg struct {
	Type   string `json:"type"`
	TurnID string `json:"turn_id"`
	Card   Card   `json:"card"`
}

// NewCard builds a card message.
func NewCard(turnID string, c Card) CardMsg {
	if len(c.Data) == 0 {
		c.Data = json.RawMessage("null")
	}
	return CardMsg{TypeCard, turnID, c}
}

// TurnUsage is the token and cost total for a turn.
type TurnUsage struct {
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
}

// TurnEnd closes a turn.
type TurnEnd struct {
	Type   string    `json:"type"`
	TurnID string    `json:"turn_id"`
	Status string    `json:"status"`
	Text   string    `json:"text"`
	Model  string    `json:"model"`
	Usage  TurnUsage `json:"usage"`
}

// NewTurnEnd builds a turn_end message.
func NewTurnEnd(turnID, status, text, model string, u TurnUsage) TurnEnd {
	return TurnEnd{TypeTurnEnd, turnID, status, text, model, u}
}

// Error is a problem the user should see.
type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	InputID string `json:"input_id,omitempty"`
}

// NewError builds an error message.
func NewError(code, message, inputID string) Error {
	return Error{TypeError, code, message, inputID}
}

// HistoryMessage is one entry of GET /api/conversation/messages.
type HistoryMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// Client-to-server message types.
const (
	TypeSay       = "say"
	TypePTTStart  = "ptt_start"
	TypePTTEnd    = "ptt_end"
	TypePTTCancel = "ptt_cancel"
	TypeInterrupt = "interrupt"
)

// ClientMessage is any client-to-server text frame; fields not used by a type are zero.
type ClientMessage struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Text       string `json:"text"`
	SampleRate int    `json:"sample_rate"`
	HeardSeq   *int   `json:"heard_seq"`
}

// Limits from PROTOCOL.md.
const (
	MaxTextChars      = 4000
	MaxTextFrameBytes = 16 << 10
	MaxBinaryFrame    = 64 << 10
	PTTSampleRate     = 16000
	MaxPTTSeconds     = 60
)
