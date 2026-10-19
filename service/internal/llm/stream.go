package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Stream is one in-flight completion. It is not safe for concurrent use.
type Stream struct {
	body io.Closer
	sse  *sseReader

	pending []Event
	done    bool
	err     error

	finish    string
	usage     Usage
	cost      *float64
	tier      string
	requestID string
	model     string
	calls     map[int]*ToolCall
}

func newStream(body io.ReadCloser, requestID, tier string) *Stream {
	s := &Stream{body: body, requestID: requestID, tier: tier, calls: map[int]*ToolCall{}}
	s.sse = newSSEReader(body, s.comment)
	return s
}

// comment picks the request cost out of Neuralwatt's `: cost {...}` keepalive
// comments; every other comment (energy, pings) is ignored.
func (s *Stream) comment(c string) {
	rest, ok := strings.CutPrefix(c, "cost ")
	if !ok {
		return
	}
	var v struct {
		RequestCostUSD *float64 `json:"request_cost_usd"`
	}
	if json.Unmarshal([]byte(rest), &v) == nil && v.RequestCostUSD != nil {
		s.cost = v.RequestCostUSD
	}
}

// Recv returns the next event. After the EventDone event it returns io.EOF.
func (s *Stream) Recv() (Event, error) {
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
		if s.err != nil {
			return Event{}, s.err
		}
		if s.done {
			return Event{}, io.EOF
		}
		if err := s.step(); err != nil {
			s.err = err
		}
	}
}

// Close releases the connection. It is safe to call more than once.
func (s *Stream) Close() error {
	if err := s.body.Close(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}
	return nil
}

// step consumes one SSE event.
func (s *Stream) step() error {
	data, event, err := s.sse.next()
	if err != nil {
		return wrapReadErr(err)
	}
	if event == "error" {
		return classifyStreamError([]byte(data))
	}
	if strings.TrimSpace(data) == "[DONE]" {
		return s.finishStream()
	}
	var c chunk
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return &Error{Kind: KindProtocol, Message: "malformed chunk", Err: err}
	}
	if len(c.Error) > 0 && string(c.Error) != "null" {
		return classifyStreamError([]byte(data))
	}
	s.apply(&c)
	return nil
}

func (s *Stream) apply(c *chunk) {
	if s.requestID == "" {
		s.requestID = c.ID
	}
	if s.tier == "" {
		s.tier = c.ServiceTier
	}
	if c.Model != "" {
		s.model = c.Model
	}
	if u := c.Usage; u != nil {
		s.usage = Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens}
		if u.PromptTokensDetails != nil {
			s.usage.CachedTokens = u.PromptTokensDetails.CachedTokens
		}
		if u.Cost != nil {
			s.cost = u.Cost
		}
	}
	if c.Cost != nil {
		s.cost = c.Cost
	}
	for i := range c.Choices {
		ch := &c.Choices[i]
		if ch.Index != 0 {
			continue
		}
		if ch.Delta.Content != "" {
			s.pending = append(s.pending, Event{Kind: EventText, Text: ch.Delta.Content})
		}
		for _, f := range ch.Delta.ToolCalls {
			s.addFragment(f)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.finish = *ch.FinishReason
		}
	}
}

// addFragment merges one streamed tool-call fragment. Fragments of the same
// call share an index; the id and name arrive once, arguments in pieces.
func (s *Stream) addFragment(f wireToolCall) {
	idx := 0
	if f.Index != nil {
		idx = *f.Index
	}
	tc := s.calls[idx]
	if tc == nil {
		tc = &ToolCall{}
		s.calls[idx] = tc
	}
	if f.ID != "" {
		tc.ID = f.ID
	}
	if f.Function.Name != "" {
		tc.Name = f.Function.Name
	}
	tc.Arguments += f.Function.Arguments
}

func (s *Stream) finishStream() error {
	if s.finish == "" {
		return &Error{Kind: KindTransient, Message: "stream ended without a finish reason", Err: io.ErrUnexpectedEOF}
	}
	idxs := make([]int, 0, len(s.calls))
	for i := range s.calls {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		tc := *s.calls[i]
		if tc.Name == "" {
			return &Error{Kind: KindProtocol, Err: errors.New("tool call without a name")}
		}
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", i)
		}
		s.pending = append(s.pending, Event{Kind: EventToolCall, ToolCall: tc})
	}
	s.pending = append(s.pending, Event{Kind: EventDone, Result: &Result{
		FinishReason: s.finish,
		Usage:        s.usage,
		CostUSD:      s.cost,
		ServiceTier:  s.tier,
		RequestID:    s.requestID,
		Model:        s.model,
	}})
	s.done = true
	return nil
}
