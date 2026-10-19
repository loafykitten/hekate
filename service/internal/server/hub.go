package server

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// sendQueue is how many frames a client may have waiting. A segment with audio
// is the largest frame; this is generous for a few sentences of lag and small
// enough that a stalled phone is dropped rather than buffered forever.
const sendQueue = 64

// Hub fans events out to every connected WebSocket client. Broadcast never
// blocks: a client whose queue is full is dropped.
type Hub struct {
	log *slog.Logger

	mu      sync.Mutex
	clients map[*client]struct{}
	closed  bool
}

// NewHub creates an empty hub.
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{log: log, clients: map[*client]struct{}{}}
}

// client is one connection's outbound side.
type client struct {
	id   string
	send chan []byte
	// kick closes the connection; it must not block.
	kick func()
	once sync.Once
}

// enqueue adds a frame without blocking. It reports false (and drops the
// client) when the queue is full.
func (c *client) enqueue(frame []byte) bool {
	select {
	case c.send <- frame:
		return true
	default:
		c.once.Do(c.kick)
		return false
	}
}

// Broadcast sends msg to every client. msg is any value from package proto.
func (h *Hub) Broadcast(msg any) {
	frame, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("cannot encode event", "error", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.enqueue(frame) {
			h.log.Warn("dropping slow client", "client_id", c.id)
		}
	}
}

// add registers a client; it reports false once the hub is closed.
func (h *Hub) add(c *client) bool {
	return h.addWithFirst(c, nil)
}

// addWithFirst registers c with first() as its first frame. Both happen under
// the broadcast lock, so a broadcast either reaches c after that frame or
// happened before first() ran (and first can see its effects).
func (h *Hub) addWithFirst(c *client, first func() any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if first != nil {
		frame, err := json.Marshal(first())
		if err != nil {
			h.log.Error("cannot encode event", "error", err)
			return false
		}
		c.enqueue(frame)
	}
	h.clients[c] = struct{}{}
	return true
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

// Clients returns how many clients are connected.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Close disconnects every client and refuses new ones.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	cs := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		cs = append(cs, c)
	}
	h.mu.Unlock()
	for _, c := range cs {
		c.once.Do(c.kick)
	}
}
