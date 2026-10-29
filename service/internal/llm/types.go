// Package llm is a streaming client for Neuralwatt's OpenAI-compatible chat
// completions API. It only streams: the hosted flex tier requires it, and
// Hekate speaks sentence by sentence anyway.
package llm

import "encoding/json"

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one chat message. Assistant messages may carry ToolCalls; tool
// messages answer one of them through ToolCallID.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// Images are data: URLs a user message shows the model, after Content. Only vision models
	// take them.
	Images []string
}

// ToolCall is a fully assembled function call requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON as sent by the model
}

// Tool describes a callable function by its JSON schema.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request is one chat completion. Effort is always sent: the provider's
// default is "max", which is far too slow for conversation.
type Request struct {
	Model     string
	Effort    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Usage is token accounting for one request.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
}

// Result is the final outcome of a stream.
type Result struct {
	FinishReason string   // stop, tool_calls, length, ...
	Usage        Usage    //
	CostUSD      *float64 // nil when the provider did not report a cost
	ServiceTier  string   // standard, flex, ...
	RequestID    string
	Model        string
}

// EventKind says which field of an Event is set.
type EventKind int

// Event kinds.
const (
	EventText EventKind = iota + 1
	EventToolCall
	EventDone
)

// Event is one item from a Stream.
type Event struct {
	Kind     EventKind
	Text     string   // EventText: a visible content delta
	ToolCall ToolCall // EventToolCall: complete, never a fragment
	Result   *Result  // EventDone
}

// wire types

type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"` // nil, a string, or []wirePart with images
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wirePart struct {
	Type     string   `json:"type"`
	Text     string   `json:"text,omitempty"`
	ImageURL *wireURL `json:"image_url,omitempty"`
}

type wireURL struct {
	URL string `json:"url"`
}

type wireToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type wireRequest struct {
	Model         string        `json:"model"`
	Effort        string        `json:"reasoning_effort"`
	Stream        bool          `json:"stream"`
	StreamOptions streamOptions `json:"stream_options"`
	Messages      []wireMessage `json:"messages"`
	Tools         []wireTool    `json:"tools,omitempty"`
	MaxTokens     int           `json:"max_tokens,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (r Request) wire() wireRequest {
	w := wireRequest{
		Model:         r.Model,
		Effort:        r.Effort,
		Stream:        true,
		StreamOptions: streamOptions{IncludeUsage: true},
		MaxTokens:     r.MaxTokens,
		Messages:      make([]wireMessage, 0, len(r.Messages)),
	}
	for _, m := range r.Messages {
		wm := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		// An assistant turn that is only tool calls has null content.
		switch {
		case len(m.Images) > 0:
			parts := []wirePart{{Type: "text", Text: m.Content}}
			for _, url := range m.Images {
				parts = append(parts, wirePart{Type: "image_url", ImageURL: &wireURL{URL: url}})
			}
			wm.Content = parts
		case m.Content != "" || len(m.ToolCalls) == 0:
			wm.Content = m.Content
		}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if args == "" {
				args = "{}"
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID: tc.ID, Type: "function", Function: wireFunction{Name: tc.Name, Arguments: args},
			})
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range r.Tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		w.Tools = append(w.Tools, wt)
	}
	return w
}

// chunk is one streamed completion chunk.
type chunk struct {
	ID          string          `json:"id"`
	Model       string          `json:"model"`
	ServiceTier string          `json:"service_tier"`
	Choices     []chunkChoice   `json:"choices"`
	Usage       *chunkUsage     `json:"usage"`
	Cost        *float64        `json:"cost"`
	Error       json.RawMessage `json:"error"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason"`
	Delta        struct {
		Content   string         `json:"content"`
		ToolCalls []wireToolCall `json:"tool_calls"`
		// reasoning / reasoning_content are deliberately not decoded: reasoning
		// text is never shown, spoken or stored.
	} `json:"delta"`
}

type chunkUsage struct {
	PromptTokens        int      `json:"prompt_tokens"`
	CompletionTokens    int      `json:"completion_tokens"`
	Cost                *float64 `json:"cost"` // OpenRouter-style; Neuralwatt sends a comment instead
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
