package agent

// Hub delivers server events to every connected client. Broadcast must never
// block: a client that cannot keep up is dropped by the hub, not waited for.
// msg is any value from package proto.
type Hub interface {
	Broadcast(msg any)
}
