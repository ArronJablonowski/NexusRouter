package codexbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexrpc"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func importedSessionMessages() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "Original task"},
		{Role: "assistant", Content: "Previously delegated.", ToolCalls: []providers.ToolCall{{ID: "old-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"old work","validation":"text"}`)}}},
		{Role: "tool", ToolCallID: "old-call", Content: `{"untrusted_output":"saved worker result"}`},
		{Role: "user", Content: "Review the saved result."},
	}
}

func historySessionPrefix() []codexrpc.Envelope {
	prefix := sessionPrefix()
	return []codexrpc.Envelope{prefix[0], prefix[1], sessionResponse("4", `{}`), prefix[2]}
}

func TestSessionImportsHistoryBeforeNewTurn(t *testing.T) {
	s, w, req := newSessionFixture(t, append(historySessionPrefix(), sessionFinal()...))
	req.Messages = importedSessionMessages()
	var out []providers.Chunk
	if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	writes := w.sent()
	if len(writes) != 5 {
		t.Fatal("unexpected requests", len(writes))
	}
	for i, method := range []string{"initialize", "initialized", "thread/start", "thread/inject_items", "turn/start"} {
		if writes[i].Method != method {
			t.Fatal("history/turn ordering", i, writes[i].Method)
		}
	}
	var thread struct {
		Ephemeral bool `json:"ephemeral"`
	}
	if json.Unmarshal(writes[2].Params, &thread) != nil || !thread.Ephemeral {
		t.Fatal("import did not use new ephemeral thread")
	}
	var injected struct {
		ThreadID string                       `json:"threadId"`
		Items    []map[string]json.RawMessage `json:"items"`
	}
	if json.Unmarshal(writes[3].Params, &injected) != nil || injected.ThreadID != "thread-1" || len(injected.Items) != 4 {
		t.Fatal("incomplete native history")
	}
	if string(injected.Items[2]["type"]) != `"function_call"` || string(injected.Items[2]["call_id"]) != `"old-call"` || string(injected.Items[3]["type"]) != `"function_call_output"` || string(injected.Items[3]["call_id"]) != `"old-call"` {
		t.Fatal("native tool pair lost")
	}
	if strings.Contains(string(writes[3].Params), "Review the saved result.") {
		t.Fatal("new prompt injected as old history")
	}
	var turn struct {
		Input []struct {
			Text string `json:"text"`
		} `json:"input"`
	}
	if json.Unmarshal(writes[4].Params, &turn) != nil || len(turn.Input) != 1 || turn.Input[0].Text != "Review the saved result." {
		t.Fatal("prior history flattened into new prompt")
	}
	for _, c := range out {
		if c.ToolCall != nil {
			t.Fatal("historical tool call re-emitted")
		}
	}
	if len(out) == 0 || !out[len(out)-1].Done || out[len(out)-1].FinishReason != "stop" {
		t.Fatal("missing completion")
	}
}

func TestSessionImportedHistoryKeepsNewToolExchangeBound(t *testing.T) {
	frames := append(historySessionPrefix(), sessionTool()...)
	frames = append(frames, sessionToolCompletion())
	frames = append(frames, sessionFinal()...)
	s, w, req := newSessionFixture(t, frames)
	req.Messages = importedSessionMessages()
	var out []providers.Chunk
	if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	var call *providers.ToolCall
	for _, c := range out {
		if c.ToolCall != nil {
			call = c.ToolCall
		}
	}
	if call == nil || call.ID != "call-1" || len(w.sent()) != 5 {
		t.Fatal("new proposal not paused")
	}
	next := req
	next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{*call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: "new result"})
	if err := s.Stream(context.Background(), next, func(providers.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	writes := w.sent()
	if len(writes) != 6 || writes[5].Method != "" || string(writes[5].ID) != "50" {
		t.Fatal("tool resume repeated history import or turn dispatch")
	}
}

func TestSessionImportFailureDoesNotStartTurn(t *testing.T) {
	for name, rejected := range map[string]codexrpc.Envelope{
		"null ack":           sessionResponse("4", `null`),
		"unknown ack":        sessionResponse("4", `{"ok":true}`),
		"wrong id":           sessionResponse("other", `{}`),
		"error":              {ID: json.RawMessage("4"), Error: &codexrpc.RemoteError{Code: -32601, Message: "private backend failure"}},
		"tool during import": {ID: json.RawMessage("50"), Method: "item/tool/call", Params: json.RawMessage(`{}`)},
		"foreign status":     sessionNotice("thread/status/changed", `{"threadId":"other"}`),
		"foreign start":      sessionNotice("thread/started", `{"thread":{"id":"other"}}`),
		"unknown notice":     sessionNotice("unknown/private", `{}`),
	} {
		t.Run(name, func(t *testing.T) {
			prefix := sessionPrefix()
			s, w, req := newSessionFixture(t, []codexrpc.Envelope{prefix[0], prefix[1], rejected})
			req.Messages = importedSessionMessages()
			err := s.Stream(context.Background(), req, func(providers.Chunk) error { t.Error("import emitted model output"); return nil })
			if err == nil || strings.Contains(err.Error(), "private") || !s.closed.Load() {
				t.Fatal("import failure did not close safely", err)
			}
			for _, e := range w.sent() {
				if e.Method == "turn/start" {
					t.Fatal("failed import started generation")
				}
			}
		})
	}
}

func TestSessionInvalidHistoryDoesNotTransmit(t *testing.T) {
	s, w, req := newSessionFixture(t, nil)
	req.Messages = importedSessionMessages()[:2] // Unmatched tool call; no new user input.
	if err := s.Stream(context.Background(), req, func(providers.Chunk) error { return nil }); err == nil || len(w.sent()) != 0 {
		t.Fatal("invalid history transmitted")
	}
}

func TestSessionImportAcceptsBoundDelayedThreadNotices(t *testing.T) {
	for _, notice := range []codexrpc.Envelope{
		sessionNotice("thread/started", `{"thread":{"id":"thread-1"}}`),
		sessionNotice("thread/status/changed", `{"threadId":"thread-1","status":{"type":"idle"}}`),
	} {
		prefix := historySessionPrefix()
		frames := append(append([]codexrpc.Envelope{}, prefix[:2]...), notice)
		frames = append(frames, prefix[2:]...)
		frames = append(frames, sessionFinal()...)
		s, _, req := newSessionFixture(t, frames)
		req.Messages = importedSessionMessages()
		if err := s.Stream(context.Background(), req, func(providers.Chunk) error { return nil }); err != nil {
			t.Fatal("bound delayed notice rejected", notice.Method, err)
		}
	}
}

func TestSessionImportNoticesRequireVerifiedControls(t *testing.T) {
	for _, checked := range []bool{false, true} {
		frames := []codexrpc.Envelope{
			sessionNotice("deprecationNotice", "{\"summary\":\"`[features].use_legacy_landlock` is deprecated and will be removed soon.\"}"),
			sessionResponse("4", `{}`),
		}
		s, w, _ := newSessionFixture(t, frames)
		s.thread = "thread-1"
		if checked {
			s.launchFeatures = []string{"use_legacy_landlock"}
		}
		_, err := s.call("4", "thread/inject_items", map[string]any{"threadId": s.thread, "items": []any{}})
		if (err == nil) != checked || len(w.sent()) != 1 {
			t.Fatal("informational notice controls changed", checked, err)
		}
	}
}
