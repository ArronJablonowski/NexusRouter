package codexbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// scriptedSessionWire never starts a process or performs inference. Keeping
// writes separately observable proves that proposing a tool does not execute it.
type scriptedSessionWire struct {
	mu     sync.Mutex
	frames []codexrpc.Envelope
	writes []codexrpc.Envelope
	closed chan struct{}
	once   sync.Once
	block  bool
}

func (w *scriptedSessionWire) Read() (codexrpc.Envelope, error) {
	w.mu.Lock()
	if len(w.frames) > 0 {
		e := w.frames[0]
		w.frames = w.frames[1:]
		w.mu.Unlock()
		return e, nil
	}
	block := w.block
	w.mu.Unlock()
	if block {
		<-w.closed
	}
	return codexrpc.Envelope{}, io.EOF
}
func (w *scriptedSessionWire) Write(e codexrpc.Envelope) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, e)
	return nil
}
func (w *scriptedSessionWire) Close() error { w.once.Do(func() { close(w.closed) }); return nil }
func (w *scriptedSessionWire) sent() []codexrpc.Envelope {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]codexrpc.Envelope(nil), w.writes...)
}
func sessionResponse(id, result string) codexrpc.Envelope {
	return codexrpc.Envelope{ID: json.RawMessage(id), Result: json.RawMessage(result)}
}
func sessionNotice(method, params string) codexrpc.Envelope {
	return codexrpc.Envelope{Method: method, Params: json.RawMessage(params)}
}
func sessionPrefix() []codexrpc.Envelope {
	return []codexrpc.Envelope{
		sessionResponse("1", `{"userAgent":"fixture"}`),
		sessionResponse("2", `{"model":"gpt-5.6-sol","thread":{"id":"thread-1"}}`),
		sessionResponse("3", `{"turn":{"id":"turn-1","status":"inProgress"}}`),
	}
}
func sessionFinal() []codexrpc.Envelope {
	return []codexrpc.Envelope{
		sessionNotice("item/started", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"answer-1","text":"","phase":"final_answer"}}`),
		sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"answer-1","text":"Reviewed local result.","phase":"final_answer"}}`),
		sessionNotice("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`),
	}
}
func sessionTool() []codexrpc.Envelope {
	return []codexrpc.Envelope{
		sessionNotice("item/started", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"inProgress"}}`),
		{ID: json.RawMessage("50"), Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","callId":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"}}`)},
	}
}
func newSessionFixture(t *testing.T, frames []codexrpc.Envelope) (*Session, *scriptedSessionWire, providers.Request) {
	t.Helper()
	w := &scriptedSessionWire{frames: frames, closed: make(chan struct{})}
	s, err := NewSession(context.Background(), w, Options{Model: "gpt-5.6-sol", CWD: t.TempDir(), ReasoningEffort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if len(w.sent()) != 0 {
		t.Fatal("constructor sent inference traffic")
	}
	return s, w, providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "Delegate this task."}}, Tools: []providers.Tool{{Name: "delegate", Description: "Request local work", Parameters: json.RawMessage(`{"type":"object"}`)}}}
}
func collectSession(out *[]providers.Chunk) func(providers.Chunk) error {
	return func(c providers.Chunk) error { *out = append(*out, c); return nil }
}
func TestSessionFinalAnswerAndHandshake(t *testing.T) {
	s, w, req := newSessionFixture(t, append(sessionPrefix(), sessionFinal()...))
	models, err := s.Models(context.Background())
	if err != nil || len(models) != 1 || models[0] != req.Model || len(w.sent()) != 0 {
		t.Fatalf("model discovery: %v %v", models, err)
	}
	var out []providers.Chunk
	if err = s.Stream(context.Background(), req, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	var text string
	for _, c := range out {
		text += c.Text
	}
	if text != "Reviewed local result." || len(out) == 0 || !out[len(out)-1].Done || out[len(out)-1].FinishReason != "stop" {
		t.Fatalf("invalid completion: %+v", out)
	}
	writes := w.sent()
	if len(writes) != 4 {
		t.Fatalf("writes: %+v", writes)
	}
	for i, method := range []string{"initialize", "initialized", "thread/start", "turn/start"} {
		if writes[i].Method != method {
			t.Fatalf("write %d: %+v", i, writes[i])
		}
	}
	if string(writes[0].ID) != "1" || string(writes[2].ID) != "2" || string(writes[3].ID) != "3" {
		t.Fatal("unexpected handshake IDs")
	}
	var init struct {
		Capabilities struct {
			Experimental bool `json:"experimentalApi"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(writes[0].Params, &init) != nil || !init.Capabilities.Experimental {
		t.Fatal("dynamic tools lack experimental capability")
	}
	var thread struct {
		Model        string `json:"model"`
		DynamicTools []struct {
			Type, Name, Description string
			Tools                   []struct {
				Type, Name, Description string
				InputSchema             json.RawMessage `json:"inputSchema"`
			}
		} `json:"dynamicTools"`
	}
	if json.Unmarshal(writes[2].Params, &thread) != nil || thread.Model != req.Model || len(thread.DynamicTools) != 1 || thread.DynamicTools[0].Name != "darwin" || thread.DynamicTools[0].Type != "namespace" || len(thread.DynamicTools[0].Tools) != 1 {
		t.Fatalf("incorrect thread start: %s", writes[2].Params)
	}
	var turn struct {
		Effort string `json:"effort"`
	}
	if json.Unmarshal(writes[3].Params, &turn) != nil || turn.Effort != "medium" {
		t.Fatal("configured reasoning effort missing from initial turn")
	}
	tool := thread.DynamicTools[0].Tools[0]
	if tool.Type != "function" || tool.Name != "delegate" || tool.Description != req.Tools[0].Description || string(tool.InputSchema) != string(req.Tools[0].Parameters) {
		t.Fatalf("incorrect nested tool definition: %s", writes[2].Params)
	}
}

func TestSessionRejectsOutputTokenCeilingBeforeWriting(t *testing.T) {
	for _, limit := range []int64{-1, 1, providers.MaxOutputTokens} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			s, w, req := newSessionFixture(t, append(sessionPrefix(), sessionFinal()...))
			req.MaxOutputTokens = limit
			if err := s.Stream(context.Background(), req, func(providers.Chunk) error {
				t.Fatal("unsupported bounded request emitted output")
				return nil
			}); err == nil {
				t.Fatal("unsupported output-token ceiling accepted")
			}
			if writes := w.sent(); len(writes) != 0 {
				t.Fatalf("unsupported output-token ceiling wrote to Codex: %+v", writes)
			}
		})
	}
}
func TestSessionToolPausesUntilMatchingContinuation(t *testing.T) {
	frames := append(sessionPrefix(), sessionTool()...)
	frames = append(frames, sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"completed","success":true}}`))
	frames = append(frames, sessionFinal()...)
	s, w, req := newSessionFixture(t, frames)
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
	if call == nil || call.ID != "call-1" || call.Name != "delegate" || !out[len(out)-1].Done || out[len(out)-1].FinishReason != "tool_calls" {
		t.Fatalf("proposal: %+v", out)
	}
	if len(w.sent()) != 4 {
		t.Fatal("tool request answered before runtime result")
	}
	next := req
	next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{*call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: `{"untrusted_output":"Done"}`})
	out = nil
	if err := s.Stream(context.Background(), next, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	writes := w.sent()
	if len(writes) != 5 || string(writes[4].ID) != "50" || writes[4].Method != "" {
		t.Fatalf("continuation writes: %+v", writes)
	}
	var result struct {
		Success      bool
		ContentItems []struct{ Type, Text string }
	}
	if json.Unmarshal(writes[4].Result, &result) != nil || !result.Success || len(result.ContentItems) != 1 || result.ContentItems[0].Type != "inputText" || result.ContentItems[0].Text != next.Messages[2].Content {
		t.Fatalf("response: %s", writes[4].Result)
	}
	if len(out) == 0 || !out[len(out)-1].Done || out[len(out)-1].FinishReason != "stop" {
		t.Fatalf("final: %+v", out)
	}
}
func TestSessionMismatchNeverAnswersTool(t *testing.T) {
	s, w, req := newSessionFixture(t, append(sessionPrefix(), sessionTool()...))
	var out []providers.Chunk
	if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	if err := s.Stream(context.Background(), req, collectSession(&out)); err == nil {
		t.Fatal("accepted missing tool result")
	}
	if len(w.sent()) != 4 {
		t.Fatal("invalid continuation sent response")
	}
}
func TestSessionFailuresNeverEmitSuccess(t *testing.T) {
	tests := map[string][]codexrpc.Envelope{
		"wrong model":            {sessionPrefix()[0], sessionResponse("2", `{"model":"different-model","thread":{"id":"thread-1"}}`)},
		"wrong initialize id":    {sessionResponse("99", `{}`)},
		"EOF":                    sessionPrefix(),
		"unknown server request": append(sessionPrefix(), codexrpc.Envelope{ID: json.RawMessage("99"), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{}`)}),
		"failed turn":            append(sessionPrefix(), sessionNotice("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"sensitive provider error"}}}`)),
		"wrong thread":           append(sessionPrefix(), sessionNotice("turn/completed", `{"threadId":"another","turn":{"id":"turn-1","status":"completed"}}`)),
		"wrong turn":             append(sessionPrefix(), sessionNotice("turn/completed", `{"threadId":"thread-1","turn":{"id":"another","status":"completed"}}`)),
		"malformed completion":   append(sessionPrefix(), sessionNotice("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1"}}`)),
	}
	for name, frames := range tests {
		t.Run(name, func(t *testing.T) {
			s, _, req := newSessionFixture(t, frames)
			var out []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&out)); err == nil {
				t.Fatal("failure accepted")
			} else if strings.Contains(err.Error(), "sensitive provider error") {
				t.Fatal("provider error payload escaped redaction boundary")
			}
			for _, c := range out {
				if c.Done {
					t.Fatalf("failure emitted completion: %+v", out)
				}
			}
		})
	}
}
func TestSessionCancellationClosesBlockedWire(t *testing.T) {
	s, w, req := newSessionFixture(t, nil)
	w.block = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Stream(ctx, req, func(providers.Chunk) error { return nil }) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock read")
	}
	select {
	case <-w.closed:
	default:
		t.Fatal("wire not closed")
	}
}
func TestSessionCallbackFailureClosesWire(t *testing.T) {
	s, w, req := newSessionFixture(t, append(sessionPrefix(), sessionFinal()...))
	want := errors.New("consumer stopped")
	if err := s.Stream(context.Background(), req, func(providers.Chunk) error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error: %v", err)
	}
	select {
	case <-w.closed:
	default:
		t.Fatal("wire not closed")
	}
}
