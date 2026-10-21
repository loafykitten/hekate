package proto

import "encoding/json"

// Menu data domains named by invalidate events.
const (
	DomainTasks     = "tasks"
	DomainReminders = "reminders"
	DomainCalendar  = "calendar"
	DomainMemory    = "memory"
	DomainWeather   = "weather"
	DomainMail      = "mail"
	DomainMoney     = "money"
	DomainApprovals = "approvals"
	DomainChanges   = "changes"
)

// Notify kinds.
const (
	NotifyKindReminder = "reminder"
	NotifyKindApproval = "approval"
	NotifyKindChange   = "change"
	NotifyKindNudge    = "nudge"   // the heartbeat: something needs the owner
	NotifyKindCheckIn  = "checkin" // the evening check-in: tapping it starts one
)

// Notify tells clients that something fired, even with no turn running.
type Notify struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Card  *Card  `json:"card,omitempty"`
}

// NewNotify builds a notify message.
func NewNotify(id, kind, title, body string, card *Card) Notify {
	return Notify{TypeNotify, id, kind, title, body, card}
}

// Invalidate says menu data changed and should be refetched.
type Invalidate struct {
	Type    string   `json:"type"`
	Domains []string `json:"domains"`
}

// NewInvalidate builds an invalidate message; no domains is sent as [].
func NewInvalidate(domains ...string) Invalidate {
	if domains == nil {
		domains = []string{}
	}
	return Invalidate{TypeInvalidate, domains}
}

// ApprovalMsg says an approval was created or changed. The approval is already
// encoded because its type lives in package approvals, which imports proto.
type ApprovalMsg struct {
	Type     string          `json:"type"`
	Approval json.RawMessage `json:"approval"`
}

// NewApproval builds an approval message from the encoded Approval.
func NewApproval(approval json.RawMessage) ApprovalMsg { return ApprovalMsg{TypeApproval, approval} }

// Events is how feature packages emit server-to-client events without
// importing the server. The hub implements it.
type Events interface {
	// Invalidate tells every client that the named domains changed.
	Invalidate(domains ...string)
	// Notify shows n on every connected client.
	Notify(n Notify)
}

// NopEvents drops every event, for packages used without a hub.
type NopEvents struct{}

// Invalidate implements Events.
func (NopEvents) Invalidate(...string) {}

// Notify implements Events.
func (NopEvents) Notify(Notify) {}
