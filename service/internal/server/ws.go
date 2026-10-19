package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/loafykitten/hekate/service/internal/proto"
	"github.com/loafykitten/hekate/service/internal/voice"
)

const (
	pingInterval   = 20 * time.Second // PROTOCOL.md
	pingTimeout    = 10 * time.Second
	writeTimeout   = 10 * time.Second
	sttTimeout     = 30 * time.Second
	maxPTTSamples  = proto.PTTSampleRate * proto.MaxPTTSeconds
	minPTTSamples  = proto.PTTSampleRate / 5 // under 0.2 s is a mis-tap, not speech
	maxInputIDLen  = 128
	defaultHistory = 50
	maxHistory     = 100
)

// Conversation is the slice of the agent the HTTP layer needs.
type Conversation interface {
	ConversationID() string
	Gestures() []string
	Say(origin, inputID, text string)
	Interrupt(turnID string, heardSeq int)
	Hush()
	Messages(ctx context.Context, before int64, limit int) ([]proto.HistoryMessage, error)
}

// conversationRoutes registers the WebSocket and history endpoints.
func (s *Server) conversationRoutes(mux *http.ServeMux) {
	if s.conv == nil || s.hub == nil {
		return
	}
	mux.HandleFunc("GET /api/ws", s.handleWS)
	mux.HandleFunc("GET /api/conversation/messages", s.handleMessages)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "before must be a message id"})
			return
		}
		before = n
	}
	limit := defaultHistory
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxHistory {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and " + strconv.Itoa(maxHistory)})
			return
		}
		limit = n
	}
	msgs, err := s.conv.Messages(r.Context(), before, limit)
	if err != nil {
		s.log.Error("cannot load messages", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if msgs == nil {
		msgs = []proto.HistoryMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// session is one WebSocket connection.
type session struct {
	s      *Server
	conn   *websocket.Conn
	cl     *client
	ctx    context.Context //nolint:containedctx // the connection's lifetime
	cancel context.CancelFunc
	id     string

	// push-to-talk state, touched only by the read loop
	pttID    string
	pttGen   uint64 // input number of this push-to-talk, see Server.inputs
	pcm      []int16
	carry    byte // odd byte left over from the previous frame
	hasCarry bool

	wg sync.WaitGroup // transcriptions in flight
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Accept checks that Origin matches Host, which is the same-origin rule.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept already replied
	}
	conn.SetReadLimit(proto.MaxBinaryFrame)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ss := &session{s: s, conn: conn, ctx: ctx, cancel: cancel, id: newClientID()}
	ss.cl = &client{id: ss.id, send: make(chan []byte, sendQueue), kick: func() {
		cancel()
		_ = conn.CloseNow()
	}}
	log := s.log.With("client_id", ss.id)

	// ready goes first, before the hub can queue anything else for this client,
	// and its capabilities are read under the broadcast lock so a capabilities
	// broadcast can't slip between the snapshot and registration.
	gestures := s.conv.Gestures() // may read the manifest; keep it outside the lock
	if !s.hub.addWithFirst(ss.cl, func() any {
		return proto.NewReady(ss.id, s.conv.ConversationID(), s.capabilities(), gestures)
	}) {
		_ = conn.Close(websocket.StatusGoingAway, "shutting down")
		return
	}
	defer s.hub.remove(ss.cl)
	log.Info("client connected", "clients", s.hub.Clients())

	go ss.writeLoop(log)
	go ss.pingLoop()

	ss.readLoop(log)
	cancel()
	ss.wg.Wait()
	_ = conn.CloseNow()
	log.Info("client disconnected")
}

func (s *Server) capabilities() proto.Capabilities {
	return proto.Capabilities{TTS: s.tts, STT: s.stt != nil && s.stt.Ready()}
}

// sendTo queues a message for this client only.
func (ss *session) sendTo(msg any) {
	frame, err := json.Marshal(msg)
	if err != nil {
		ss.s.log.Error("cannot encode message", "error", err)
		return
	}
	ss.cl.enqueue(frame)
}

func (ss *session) writeLoop(log interface{ Warn(string, ...any) }) {
	for {
		select {
		case frame := <-ss.cl.send:
			wctx, cancel := context.WithTimeout(ss.ctx, writeTimeout)
			err := ss.conn.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				if ss.ctx.Err() == nil {
					log.Warn("write failed, closing", "error", err)
				}
				ss.cl.once.Do(ss.cl.kick)
				return
			}
		case <-ss.ctx.Done():
			return
		}
	}
}

func (ss *session) pingLoop() {
	t := time.NewTicker(ss.s.pingEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			pctx, cancel := context.WithTimeout(ss.ctx, ss.s.pingWait)
			err := ss.conn.Ping(pctx)
			cancel()
			if err != nil {
				ss.cl.once.Do(ss.cl.kick)
				return
			}
		case <-ss.ctx.Done():
			return
		}
	}
}

func (ss *session) readLoop(log interface{ Warn(string, ...any) }) {
	for {
		typ, data, err := ss.conn.Read(ss.ctx)
		if err != nil {
			if ss.ctx.Err() == nil && websocket.CloseStatus(err) == -1 {
				log.Warn("read failed", "error", err)
			}
			return
		}
		switch typ {
		case websocket.MessageBinary:
			ss.onAudio(data)
		case websocket.MessageText:
			if len(data) > proto.MaxTextFrameBytes {
				_ = ss.conn.Close(websocket.StatusMessageTooBig, "text frame too large")
				return
			}
			ss.onText(data)
		}
	}
}

func (ss *session) onText(data []byte) {
	var m proto.ClientMessage
	if err := json.Unmarshal(data, &m); err != nil {
		ss.sendTo(proto.NewError(proto.CodeBadMessage, "I couldn't read that message.", ""))
		return
	}
	switch m.Type {
	case proto.TypeSay:
		ss.onSay(m)
	case proto.TypePTTStart:
		ss.onPTTStart(m)
	case proto.TypePTTEnd:
		ss.onPTTEnd(m)
	case proto.TypePTTCancel:
		if m.ID == ss.pttID {
			ss.resetPTT()
		}
	case proto.TypeInterrupt:
		heard := -1
		if m.HeardSeq != nil {
			heard = *m.HeardSeq
		}
		ss.s.conv.Interrupt(m.TurnID, heard)
	default:
		ss.sendTo(proto.NewError(proto.CodeBadMessage, "I don't know that kind of message.", m.ID))
	}
}

func validInputID(id string) bool {
	return id != "" && len(id) <= maxInputIDLen && utf8.ValidString(id)
}

func (ss *session) onSay(m proto.ClientMessage) {
	if !validInputID(m.ID) {
		ss.sendTo(proto.NewError(proto.CodeBadMessage, "Messages need an id.", ""))
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	if utf8.RuneCountInString(text) > proto.MaxTextChars {
		ss.sendTo(proto.NewError(proto.CodeTooLong, "That's too long for me to take in at once.", m.ID))
		return
	}
	ss.s.say(ss.id, m.ID, text)
}

func (ss *session) onPTTStart(m proto.ClientMessage) {
	if !validInputID(m.ID) || m.SampleRate != proto.PTTSampleRate {
		ss.sendTo(proto.NewError(proto.CodeBadMessage, "Push-to-talk needs an id and 16 kHz audio.", m.ID))
		return
	}
	ss.resetPTT()
	if !ss.sttReady() {
		ss.sendTo(proto.NewError(proto.CodeSTTUnavailable, "I can't hear you yet: speech recognition isn't ready.", m.ID))
		return
	}
	// Someone started talking: she stops now, on every device, rather than
	// after the recording is transcribed (and before her voice reaches the mic).
	ss.pttID, ss.pttGen = m.ID, ss.s.startPTT()
}

func (ss *session) sttReady() bool { return ss.s.stt != nil && ss.s.stt.Ready() }

func (ss *session) resetPTT() {
	ss.pttID, ss.pcm, ss.hasCarry = "", nil, false
}

// onAudio appends little-endian 16-bit mono PCM, cutting at 60 s.
func (ss *session) onAudio(data []byte) {
	if ss.pttID == "" {
		return // not recording (rejected start, cancelled or already ended)
	}
	if ss.hasCarry {
		data = append([]byte{ss.carry}, data...)
		ss.hasCarry = false
	}
	if len(data)%2 == 1 {
		ss.carry, ss.hasCarry = data[len(data)-1], true
		data = data[:len(data)-1]
	}
	for i := 0; i+1 < len(data) && len(ss.pcm) < maxPTTSamples; i += 2 {
		ss.pcm = append(ss.pcm, int16(binary.LittleEndian.Uint16(data[i:]))) //nolint:gosec // reinterpreting the sample bits as signed is the point
	}
}

func (ss *session) onPTTEnd(m proto.ClientMessage) {
	if m.ID == "" || m.ID != ss.pttID {
		return
	}
	id, pcm := ss.pttID, ss.pcm
	ss.resetPTT()
	gen := ss.pttGen
	ss.wg.Add(1)
	go func() {
		defer ss.wg.Done()
		ss.transcribe(id, pcm, gen)
	}()
}

// transcribe runs speech-to-text and starts a turn, unless a newer input
// (gen changed) arrived while it ran.
func (ss *session) transcribe(id string, pcm []int16, gen uint64) {
	if len(pcm) < minPTTSamples {
		ss.sendTo(proto.NewTranscript(id, ""))
		return
	}
	stt := ss.s.stt
	if stt == nil || !stt.Ready() {
		ss.sendTo(proto.NewError(proto.CodeSTTUnavailable, "I can't hear you yet: speech recognition isn't ready.", id))
		return
	}
	ctx, cancel := context.WithTimeout(ss.ctx, sttTimeout)
	defer cancel()
	text, err := stt.Transcribe(ctx, pcm, proto.PTTSampleRate)
	switch {
	case ss.ctx.Err() != nil:
		return // the client left
	case errors.Is(err, voice.ErrNotReady):
		ss.sendTo(proto.NewError(proto.CodeSTTUnavailable, "I can't hear you yet: speech recognition isn't ready.", id))
		return
	case err != nil:
		ss.s.log.Warn("transcription failed", "client_id", ss.id, "error", err)
		ss.sendTo(proto.NewError(proto.CodeSTTFailed, "I couldn't make out what you said. Try again?", id))
		return
	}
	text = strings.TrimSpace(text)
	if !ss.s.sayIfCurrent(gen, func() { ss.sendTo(proto.NewTranscript(id, text)) }, ss.id, id, text) {
		ss.s.log.Info("dropping a transcription superseded by newer input", "client_id", ss.id)
	}
}

func newClientID() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail on supported platforms
	return "c_" + hex.EncodeToString(b[:])
}
