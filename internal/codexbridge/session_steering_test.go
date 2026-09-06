package codexbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func steeringFinal(turn, item string) []codexrpc.Envelope {
	frames := sessionFinal()
	for i := range frames {
		frames[i].Params = json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(string(frames[i].Params), "turn-1", turn), "answer-1", item))
	}
	return frames
}

func steeringCompletedRequest(req providers.Request, n int) providers.Request {
	req.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", Content: "Reviewed local result."})
	for i := 0; i < n; i++ {
		req.Messages = append(req.Messages, providers.Message{Role: "user", Content: "Adjust the result."})
	}
	return req
}

func TestSessionSteeringCompletedTurnAndUsage(t *testing.T) {
	frames := sessionPrefix()
	frames = append(frames, sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":10,"outputTokens":2}}}`))
	frames = append(frames, sessionFinal()...)
	frames = append(frames, sessionResponse("1001", `{"turn":{"id":"turn-2","status":"inProgress"}}`), sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-2","tokenUsage":{"total":{"inputTokens":25,"outputTokens":5}}}`))
	frames = append(frames, steeringFinal("turn-2", "answer-2")...)
	s, w, req := newSessionFixture(t, frames)
	var out []providers.Chunk
	if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	next := steeringCompletedRequest(req, 32)
	if err := s.Stream(context.Background(), next, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	var in, output int64
	for _, chunk := range out {
		if chunk.Usage != nil {
			in += chunk.Usage.InputTokens
			output += chunk.Usage.OutputTokens
		}
	}
	if in != 25 || output != 5 {
		t.Fatal("thread usage double counted", in, output)
	}
	writes := w.sent()
	last := writes[len(writes)-1]
	var params map[string]any
	if last.Method != "turn/start" || string(last.ID) != "1001" || json.Unmarshal(last.Params, &params) != nil || params["threadId"] != "thread-1" || len(params["input"].([]any)) != 32 || params["tools"] != nil {
		t.Fatal("wrong continuation control")
	}
	if err := s.Stream(context.Background(), steeringCompletedRequest(next, 1), collectSession(&out)); err == nil || len(w.sent()) != len(writes) {
		t.Fatal("lifetime guidance limit bypassed")
	}
}

func TestSessionSteeringPausedToolAdmission(t *testing.T) {
	for _, kind := range []string{"ok", "wrong ack", "rpc error", "unknown notice", "wrong usage turn", "decreasing usage"} {
		t.Run(kind, func(t *testing.T) {
			frames := append(sessionPrefix(), sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":10,"outputTokens":2}}}`))
			frames = append(frames, sessionTool()...)
			switch kind {
			case "ok":
				frames = append(frames, sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":15,"outputTokens":3}}}`))
				frames = append(frames, sessionResponse("1001", `{"turnId":"turn-1"}`))
			case "wrong ack":
				frames = append(frames, sessionResponse("1001", `{"turnId":"other"}`))
			case "rpc error":
				frames = append(frames, codexrpc.Envelope{ID: json.RawMessage("1001"), Error: &codexrpc.RemoteError{Code: -1, Message: "fixture"}})
			case "unknown notice":
				frames = append(frames, sessionNotice("unknown/action", `{}`))
			case "wrong usage turn":
				frames = append(frames, sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"other","tokenUsage":{"total":{"inputTokens":15,"outputTokens":3}}}`))
			case "decreasing usage":
				frames = append(frames, sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":9,"outputTokens":2}}}`))
			}
			frames = append(frames, sessionToolCompletion())
			frames = append(frames, sessionFinal()...)
			s, w, req := newSessionFixture(t, frames)
			var out []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
				t.Fatal(err)
			}
			var call providers.ToolCall
			for _, c := range out {
				if c.ToolCall != nil {
					call = *c.ToolCall
				}
			}
			next := req
			next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: "host result"}, providers.Message{Role: "user", Content: "private guidance"})
			err := s.Stream(context.Background(), next, collectSession(&out))
			writes := w.sent()
			if (err == nil) != (kind == "ok") {
				t.Fatal("wrong admission", err)
			}
			if len(writes) < 5 || writes[4].Method != "turn/steer" || string(writes[4].ID) != "1001" {
				t.Fatal("missing steer")
			}
			var params map[string]any
			_ = json.Unmarshal(writes[4].Params, &params)
			if params["expectedTurnId"] != "turn-1" || params["threadId"] != "thread-1" {
				t.Fatal("unbound steer")
			}
			if kind == "ok" {
				var input, output int64
				for _, chunk := range out {
					if chunk.Usage != nil {
						input += chunk.Usage.InputTokens
						output += chunk.Usage.OutputTokens
					}
				}
				if input != 15 || output != 3 {
					t.Fatal("preack usage lost or double counted", input, output)
				}
				if len(writes) != 6 || string(writes[5].ID) != "50" || strings.Contains(string(writes[5].Result), "private guidance") || !strings.Contains(string(writes[5].Result), "host result") {
					t.Fatal("wrong tool reply")
				}
			} else {
				if len(writes) != 5 {
					t.Fatal("tool answered without ack")
				}
				select {
				case <-w.closed:
				default:
					t.Fatal("failed control retained session")
				}
			}
		})
	}
}

func TestSessionSteeringRejectsMutationAndDuplicateIdentities(t *testing.T) {
	for _, kind := range []string{"prefix", "catalog", "duplicate turn", "duplicate item"} {
		t.Run(kind, func(t *testing.T) {
			turn, item := "turn-2", "answer-2"
			if kind == "duplicate turn" {
				turn = "turn-1"
			}
			if kind == "duplicate item" {
				item = "answer-1"
			}
			frames := append(sessionPrefix(), sessionFinal()...)
			frames = append(frames, sessionResponse("1001", fmt.Sprintf(`{"turn":{"id":%q,"status":"inProgress"}}`, turn)))
			frames = append(frames, steeringFinal(turn, item)...)
			s, w, req := newSessionFixture(t, frames)
			var out []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
				t.Fatal(err)
			}
			next := steeringCompletedRequest(req, 1)
			if kind == "prefix" {
				next.Messages[0].Content = "changed"
			}
			if kind == "catalog" {
				next.Tools = append([]providers.Tool(nil), next.Tools...)
				next.Tools[0].Description = "changed"
			}
			before := len(w.sent())
			if err := s.Stream(context.Background(), next, collectSession(&out)); err == nil {
				t.Fatal("invalid continuation accepted")
			}
			if (kind == "prefix" || kind == "catalog") && len(w.sent()) != before {
				t.Fatal("mutation dispatched")
			}
		})
	}
}

func TestSessionSteeringOversizedNativeFrameNeverDispatches(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprint(paused), func(t *testing.T) {
			frames := append(sessionPrefix(), sessionFinal()...)
			if paused {
				frames = append(sessionPrefix(), sessionTool()...)
			}
			s, w, req := newSessionFixture(t, frames)
			var out []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
				t.Fatal(err)
			}
			next := steeringCompletedRequest(req, 0)
			if paused {
				var call providers.ToolCall
				for _, c := range out {
					if c.ToolCall != nil {
						call = *c.ToolCall
					}
				}
				next = req
				next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: "host result"})
			}
			for i := 0; i < 32; i++ {
				next.Messages = append(next.Messages, providers.Message{Role: "user", Content: strings.Repeat("x", 64<<10)})
			}
			before := len(w.sent())
			if err := s.Stream(context.Background(), next, collectSession(&out)); err == nil || len(w.sent()) != before {
				t.Fatal("oversized control dispatched")
			}
			select {
			case <-w.closed:
			default:
				t.Fatal("oversized session remained live")
			}
		})
	}
}

func TestSessionSteeringPreackUserLifecycle(t *testing.T) {
	start := sessionNotice("item/started", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"userMessage","id":"guidance-1"}}`)
	complete := sessionNotice("item/completed", string(start.Params))
	status := sessionNotice("thread/status/changed", `{"threadId":"thread-1","status":{"type":"active"}}`)
	for _, tc := range []struct {
		name    string
		notices []codexrpc.Envelope
		valid   bool
	}{
		{"paired and status", []codexrpc.Envelope{start, complete, status}, true},
		{"orphan completion", []codexrpc.Envelope{complete}, false},
		{"duplicate start", []codexrpc.Envelope{start, start}, false},
		{"duplicate completion", []codexrpc.Envelope{start, complete, complete}, false},
		{"foreign thread", []codexrpc.Envelope{sessionNotice(start.Method, strings.ReplaceAll(string(start.Params), "thread-1", "foreign"))}, false},
		{"foreign turn", []codexrpc.Envelope{sessionNotice(start.Method, strings.ReplaceAll(string(start.Params), "turn-1", "foreign"))}, false},
		{"wrong item type", []codexrpc.Envelope{sessionNotice(start.Method, strings.ReplaceAll(string(start.Params), "userMessage", "agentMessage"))}, false},
		{"foreign status", []codexrpc.Envelope{sessionNotice(status.Method, strings.ReplaceAll(string(status.Params), "thread-1", "foreign"))}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := append(sessionPrefix(), sessionTool()...)
			frames = append(frames, tc.notices...)
			frames = append(frames, sessionResponse("1001", `{"turnId":"turn-1"}`))
			completion := sessionToolCompletion()
			completion.Params = json.RawMessage(strings.ReplaceAll(string(completion.Params), `"success":true`, `"success":false`))
			completion.Params = json.RawMessage(strings.ReplaceAll(string(completion.Params), `"status":"completed"`, `"status":"failed"`))
			frames = append(frames, completion)
			frames = append(frames, sessionFinal()...)
			s, w, req := newSessionFixture(t, frames)
			var out []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
				t.Fatal(err)
			}
			var call providers.ToolCall
			for _, c := range out {
				if c.ToolCall != nil {
					call = *c.ToolCall
				}
			}
			next := req
			next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: "recoverable host failure", ToolFailed: true}, providers.Message{Role: "user", Content: "Try a different approach."})
			err := s.Stream(context.Background(), next, collectSession(&out))
			if (err == nil) != tc.valid {
				t.Fatal("preack lifecycle admission", err)
			}
			writes := w.sent()
			if tc.valid {
				if len(writes) != 6 || string(writes[5].ID) != "50" {
					t.Fatal("missing failed-tool reply")
				}
				var result struct {
					Success      bool
					ContentItems []struct{ Text string }
				}
				if json.Unmarshal(writes[5].Result, &result) != nil || result.Success || len(result.ContentItems) != 1 || result.ContentItems[0].Text != "recoverable host failure" {
					t.Fatal("failed tool became success", string(writes[5].Result))
				}
			} else {
				if len(writes) != 5 {
					t.Fatal("invalid notice answered tool")
				}
				select {
				case <-w.closed:
				default:
					t.Fatal("invalid notice retained session")
				}
			}
		})
	}
}
