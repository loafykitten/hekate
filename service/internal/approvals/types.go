// Package approvals holds actions that wait for the owner's say-so. A feature
// registers an Executor per action kind; Request stores the action and asks,
// or runs it at once when a standing rule covers it. Deciding runs the action
// on the service and records what happened.
package approvals

import (
	"context"
	"encoding/json"
)

// Approval statuses.
const (
	StatusPending = "pending"
	StatusRunning = "running" // allowed; the action is executing
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusDenied  = "denied"
	StatusExpired = "expired"
)

// Decisions the owner can make.
const (
	DecisionOnce   = "once"
	DecisionAlways = "always"
	DecisionDeny   = "deny"
)

// Who decided.
const (
	DecidedByOwner = "owner"
	DecidedByRule  = "rule"
)

// Request is an action a feature wants the owner to allow.
type Request struct {
	// Kind names the registered Executor that runs the action, e.g. "mail_trash".
	Kind string
	// Title is the question, e.g. "Move 14 newsletters from example.com to Trash?".
	Title string
	// Detail is optional plain text shown under the title.
	Detail string
	// Scope is what an "always" rule covers, matched exactly against later
	// requests of the same Kind, e.g. "from:@example.com". Empty means the
	// action can't become a standing rule.
	Scope string
	// ScopeLabel says Scope in words, e.g. "mail from example.com".
	ScopeLabel string
	// Payload is what the Executor needs to run the action.
	Payload json.RawMessage
	// Notify tells the owner by push and a notify event, for requests made
	// outside a conversation (e.g. the weekly mail noise report). Inside a
	// conversation the approval card already asks.
	Notify bool
}

// Executor runs an allowed action and says what happened in a short line,
// e.g. "Moved 14 messages to Trash".
type Executor func(ctx context.Context, payload json.RawMessage) (result string, err error)

// Approval is the API shape from PROTOCOL.md.
type Approval struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Detail     string  `json:"detail"`
	ScopeLabel *string `json:"scope_label"`
	Status     string  `json:"status"`
	Result     *string `json:"result"`
	DecidedBy  *string `json:"decided_by"`
	CreatedAt  string  `json:"created_at"`
	ExpiresAt  string  `json:"expires_at"`
	DecidedAt  *string `json:"decided_at"`
}

// Rule is a standing "always allow" for one kind and scope.
type Rule struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	ScopeLabel string `json:"scope_label"`
	Uses       int    `json:"uses"`
	CreatedAt  string `json:"created_at"`
}
