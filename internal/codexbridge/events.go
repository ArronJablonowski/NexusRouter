package codexbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type item struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Phase     string          `json:"phase"`
	Namespace string          `json:"namespace"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Status    string          `json:"status"`
	Success   *bool           `json:"success"`
}
type itemState struct {
	start                                    item
	delta                                    strings.Builder
	hasDelta, complete, requested, responded bool
	responseSuccess                          bool
}

func (s *Session) segment(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
	var text strings.Builder
	final := false
	candidate := false
	output := func(c providers.Chunk) error {
		if ctx.Err() != nil || s.closed.Load() {
			return failure(s.emitted)
		}
		s.emitted = true
		return emit(c)
	}
	done := func(reason string) error {
		c := providers.Chunk{Done: true, FinishReason: reason}
		if s.usageUpdated {
			c.Usage = &providers.Usage{InputTokens: s.usageTotal.InputTokens - s.usageReported.InputTokens, OutputTokens: s.usageTotal.OutputTokens - s.usageReported.OutputTokens}
			s.usageReported, s.usageUpdated = s.usageTotal, false
		}
		return output(c)
	}
	for {
		if ctx.Err() != nil || s.closed.Load() {
			return failure(s.emitted)
		}
		e, err := s.next()
		if err != nil {
			return err
		}
		kind, _ := e.Kind()
		if kind == codexrpc.Request {
			if e.Method != "item/tool/call" || final {
				return failure(s.emitted)
			}
			var call CallRequest
			if decodePayload(e.Params, &call) != nil {
				return failure(s.emitted)
			}
			state := s.items[call.CallID]
			if state == nil || state.complete || state.requested || state.start.Type != "dynamicToolCall" ||
				state.start.Tool != call.Tool || state.start.Namespace != call.Namespace || !equalJSON(state.start.Arguments, call.Arguments) {
				return failure(s.emitted)
			}
			for _, other := range s.items {
				if other.start.Type == "agentMessage" && !other.complete {
					return failure(s.emitted)
				}
			}
			p, err := NewPending(req, s.thread, s.turn, text.String(), call)
			if err != nil {
				return failure(s.emitted)
			}
			state.requested = true
			s.pending, s.pendingID, s.pendingCall = p, bytes.Clone(e.ID), call.CallID
			proposal := p.Proposal()
			if err = output(providers.Chunk{ToolCall: &proposal}); err != nil {
				return err
			}
			return done("tool_calls")
		}
		if kind != codexrpc.Notification {
			return failure(s.emitted)
		}
		if s.compatibilityNotice(e) {
			continue
		}
		var n struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
			Delta    string `json:"delta"`
			Item     item   `json:"item"`
			Thread   struct {
				ID string `json:"id"`
			} `json:"thread"`
			Turn struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
		}
		if decodePayload(e.Params, &n) != nil {
			return failure(s.emitted)
		}
		switch e.Method {
		case "thread/started":
			if n.Thread.ID != s.thread {
				return failure(s.emitted)
			}
		case "turn/started":
			if n.ThreadID != s.thread || n.Turn.ID != s.turn || n.Turn.Status != "inProgress" {
				return failure(s.emitted)
			}
		case "item/started":
			if n.ThreadID != s.thread || n.TurnID != s.turn || !validOpaque(n.Item.ID) || s.items[n.Item.ID] != nil || len(s.items) >= 256 {
				return failure(s.emitted)
			}
			switch n.Item.Type {
			case "agentMessage":
				if n.Item.Text != "" || final {
					return failure(s.emitted)
				}
				for _, other := range s.items {
					if other.start.Type == "agentMessage" && !other.complete {
						return failure(s.emitted)
					}
				}
			case "dynamicToolCall":
				if n.Item.Namespace != "darwin" || n.Item.Status != "inProgress" || final {
					return failure(s.emitted)
				}
			case "userMessage", "reasoning":
			default:
				// Observation cannot prevent effects; the launcher must remove
				// all built-in tools BEFORE inference, not rely on this rejection.
				return failure(s.emitted)
			}
			s.items[n.Item.ID] = &itemState{start: n.Item}
		case "item/agentMessage/delta":
			state := s.items[n.ItemID]
			if n.ThreadID != s.thread || n.TurnID != s.turn || state == nil || state.complete || state.start.Type != "agentMessage" || len(n.Delta) > maxExchangeBytes-state.delta.Len() || len(n.Delta) > maxExchangeBytes-text.Len() {
				return failure(s.emitted)
			}
			state.hasDelta = true
			state.delta.WriteString(n.Delta)
			text.WriteString(n.Delta)
			if err = output(providers.Chunk{Text: n.Delta}); err != nil {
				return err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded", "item/reasoning/textDelta":
			state := s.items[n.ItemID]
			if n.ThreadID != s.thread || n.TurnID != s.turn || state == nil || state.complete || state.start.Type != "reasoning" {
				return failure(s.emitted)
			}
			// Attribute and bound these frames, but never turn private reasoning
			// into answer text, a tool proposal or durable user-facing output.
		case "item/completed":
			state := s.items[n.Item.ID]
			if n.ThreadID != s.thread || n.TurnID != s.turn || state == nil || state.complete || state.start.Type != n.Item.Type {
				return failure(s.emitted)
			}
			switch n.Item.Type {
			case "agentMessage":
				if state.hasDelta && state.delta.String() != n.Item.Text {
					return failure(s.emitted)
				}
				if n.Item.Phase != "" && n.Item.Phase != "commentary" && n.Item.Phase != "final_answer" {
					return failure(s.emitted)
				}
				if !state.hasDelta {
					if len(n.Item.Text) > maxExchangeBytes-text.Len() {
						return failure(s.emitted)
					}
					text.WriteString(n.Item.Text)
					if err = output(providers.Chunk{Text: n.Item.Text}); err != nil {
						return err
					}
				}
				state.delta.Reset()
				final = n.Item.Phase == "final_answer"
				candidate = final || n.Item.Phase == ""
			case "dynamicToolCall":
				status := "failed"
				if state.responseSuccess {
					status = "completed"
				}
				if !state.responded || n.Item.Status != status || n.Item.Success == nil || *n.Item.Success != state.responseSuccess || n.Item.Namespace != state.start.Namespace || n.Item.Tool != state.start.Tool || !equalJSON(n.Item.Arguments, state.start.Arguments) {
					return failure(s.emitted)
				}
			}
			state.complete = true
		case "turn/completed":
			if n.ThreadID != s.thread || n.Turn.ID != s.turn || n.Turn.Status != "completed" || (len(n.Turn.Error) > 0 && string(n.Turn.Error) != "null") || !candidate {
				return failure(s.emitted)
			}
			for _, state := range s.items {
				if !state.complete {
					return failure(s.emitted)
				}
			}
			s.finished = true
			completed := req
			completed.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", Content: text.String()})
			s.completedRequest = &completed
			return done("stop")
		case "thread/tokenUsage/updated":
			if n.ThreadID != s.thread || n.TurnID != s.turn || s.recordUsage(e.Params) != nil {
				return failure(s.emitted)
			}
		case "serverRequest/resolved":
			// Tool completion, not this lifecycle hint, proves the result was
			// consumed. This hint cannot trigger a tool or a successful turn.
			if n.ThreadID != s.thread {
				return failure(s.emitted)
			}
		case "thread/status/changed":
			if n.ThreadID != s.thread {
				return failure(s.emitted)
			}
		default:
			return failure(s.emitted)
		}
	}
}

func equalJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	return json.Compact(&ca, a) == nil && json.Compact(&cb, b) == nil && bytes.Equal(ca.Bytes(), cb.Bytes())
}

func (s *Session) recordUsage(raw json.RawMessage) error {
	var n struct {
		TokenUsage struct {
			Total struct {
				Input  *int64 `json:"inputTokens"`
				Output *int64 `json:"outputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if decodePayload(raw, &n) != nil {
		return failure(s.emitted)
	}
	t := n.TokenUsage.Total
	if t.Input == nil || t.Output == nil || *t.Input < s.usageTotal.InputTokens || *t.Output < s.usageTotal.OutputTokens {
		return failure(s.emitted)
	}
	s.usageTotal = providers.Usage{InputTokens: *t.Input, OutputTokens: *t.Output}
	s.usageUpdated = true
	return nil
}
