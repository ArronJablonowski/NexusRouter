package codexbridge

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func validSteeringMessage(m providers.Message) bool {
	return m.Role == "user" && len(m.ToolCalls) == 0 && m.ToolCallID == "" && !m.ToolFailed && len(m.Content) > 0 && len(m.Content) <= 64<<10 && utf8.ValidString(m.Content) && strings.TrimSpace(m.Content) != ""
}

// Controls share the lifetime wire/frame budget and never retry uncertain writes.
func (s *Session) controlID() string {
	s.controlSequence++
	return strconv.Itoa(1000 + s.controlSequence)
}

func (s *Session) guidanceInput(messages []providers.Message) ([]map[string]any, error) {
	if len(messages) == 0 || len(messages) > 32-s.steered {
		return nil, failure(s.emitted)
	}
	input := make([]map[string]any, len(messages))
	for i, m := range messages {
		if !validSteeringMessage(m) {
			return nil, failure(s.emitted)
		}
		input[i] = map[string]any{"type": "text", "text": m.Content, "text_elements": []any{}}
	}
	return input, nil
}

// The host has already persisted the paired tool result and guidance before
// invoking Stream. Bind native admission to the still-paused turn, then answer
// its tool request exactly once. A failed/uncertain control closes the session.
func (s *Session) steer(ctx context.Context, messages []providers.Message) error {
	input, err := s.guidanceInput(messages)
	if err != nil {
		return err
	}
	if ctx.Err() != nil || s.closed.Load() {
		return failure(s.emitted)
	}
	result, err := s.call(s.controlID(), "turn/steer", map[string]any{"threadId": s.thread, "expectedTurnId": s.turn, "input": input})
	if err != nil {
		return err
	}
	var ack struct {
		TurnID string `json:"turnId"`
	}
	if decodePayload(result, &ack) != nil || ack.TurnID != s.turn {
		return failure(s.emitted)
	}
	s.steered += len(messages)
	return nil
}

// A completed native turn needs a new turn, not turn/steer. Only the exact
// previously emitted conversation followed by new plain user guidance qualifies;
// native thread state supplies prior context, so history/tools are not reinjected.
func (s *Session) continueCompleted(ctx context.Context, req providers.Request) error {
	if s.completedRequest == nil || len(req.Messages) <= len(s.completedRequest.Messages) {
		return failure(s.emitted)
	}
	n := len(s.completedRequest.Messages)
	prefix := req
	prefix.Messages = prefix.Messages[:n]
	if !reflect.DeepEqual(prefix, *s.completedRequest) {
		return failure(s.emitted)
	}
	input, err := s.guidanceInput(req.Messages[n:])
	if err != nil {
		return err
	}
	params := map[string]any{"threadId": s.thread, "model": req.Model, "environments": []any{}, "input": input}
	if s.options.ReasoningEffort != "" {
		params["effort"] = s.options.ReasoningEffort
	}
	if req.JSONSchema != nil {
		params["outputSchema"] = req.JSONSchema
	}
	if ctx.Err() != nil || s.closed.Load() {
		return failure(s.emitted)
	}
	result, err := s.call(s.controlID(), "turn/start", params)
	if err != nil {
		return err
	}
	var ack struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if decodePayload(result, &ack) != nil || !validOpaque(ack.Turn.ID) || ack.Turn.Status != "inProgress" || s.turnIDs[ack.Turn.ID] {
		return failure(s.emitted)
	}
	s.steered += len(req.Messages) - n
	s.turn = ack.Turn.ID
	s.turnIDs[s.turn] = true
	s.finished, s.completedRequest = false, nil
	// Usage totals and item identities remain thread-scoped. Resetting either
	// would hide duplicate items or double-charge cumulative token usage.
	return nil
}

// Only inert, identity-bound notices may precede the steer acknowledgement.
// User-item lifecycle can arrive before the RPC response; record pairing without
// converting its content to output or allowing any native action to run.
func (s *Session) steeringNotice(e codexrpc.Envelope) bool {
	if s.compatibilityNotice(e) {
		return true
	}
	var n struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     item   `json:"item"`
	}
	if decodePayload(e.Params, &n) != nil || n.ThreadID != s.thread {
		return false
	}
	if e.Method == "thread/status/changed" {
		return true
	}
	if e.Method == "thread/tokenUsage/updated" {
		return n.TurnID == s.turn && s.recordUsage(e.Params) == nil
	}
	if n.TurnID != s.turn || n.Item.Type != "userMessage" || !validOpaque(n.Item.ID) {
		return false
	}
	switch e.Method {
	case "item/started":
		if s.items[n.Item.ID] != nil || len(s.items) >= 256 {
			return false
		}
		s.items[n.Item.ID] = &itemState{start: n.Item}
		return true
	case "item/completed":
		state := s.items[n.Item.ID]
		if state == nil || state.complete || state.start.Type != "userMessage" {
			return false
		}
		state.complete = true
		return true
	}
	return false
}
