package codexbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func sessionCommentary(phase string) []codexrpc.Envelope {
	return []codexrpc.Envelope{
		sessionNotice("item/started", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"comment-1","text":""`+phase+`}}`),
		sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"comment-1","text":"Delegating."`+phase+`}}`),
	}
}

func sessionToolCompletion() codexrpc.Envelope {
	return sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"completed","success":true}}`)
}

func TestSessionOwnsCatalogBeforeCallingConsumer(t *testing.T) {
	frames := append(sessionPrefix(), sessionCommentary(`,"phase":"commentary"`)...)
	tool := sessionTool()
	for i := range tool {
		tool[i].Params = json.RawMessage(strings.ReplaceAll(string(tool[i].Params), `"delegate"`, `"shell"`))
	}
	frames = append(frames, tool...)
	s, w, req := newSessionFixture(t, frames)
	mutated := false
	err := s.Stream(context.Background(), req, func(c providers.Chunk) error {
		if c.Text != "" {
			req.Tools[0].Name = "shell"
			mutated = true
		}
		if c.ToolCall != nil || c.Done {
			t.Error("caller mutation authorized a tool outside the original catalog")
		}
		return nil
	})
	if !mutated || err == nil {
		t.Fatalf("catalog mutation protection: mutated=%v err=%v", mutated, err)
	}
	if len(w.sent()) != 4 {
		t.Fatal("unregistered tool received a response")
	}
}

func TestSessionRejectsDuplicateAndCaseAliasedControlFields(t *testing.T) {
	tests := map[string][]codexrpc.Envelope{
		"duplicate thread id": {sessionPrefix()[0], sessionResponse("2", `{"model":"gpt-5.6-sol","thread":{"id":"wrong","id":"thread-1"}}`)},
		"case aliased model":  {sessionPrefix()[0], sessionResponse("2", `{"model":"gpt-5.6-sol","MODEL":"gpt-5.6-sol","thread":{"id":"thread-1"}}`)},
		"case aliased params": append(sessionPrefix(), sessionNotice("item/started", `{"threadId":"thread-1","THREADID":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"answer-1","text":"","phase":"final_answer"}}`)),
	}
	for name, frames := range tests {
		t.Run(name, func(t *testing.T) {
			s, _, req := newSessionFixture(t, frames)
			if err := s.Stream(context.Background(), req, func(c providers.Chunk) error {
				if c.Done {
					t.Error("ambiguous control fields completed")
				}
				return nil
			}); err == nil {
				t.Fatal("ambiguous control fields accepted")
			}
		})
	}
}

func TestSessionUnknownServerRequestBeforeInitializationStopsHandshake(t *testing.T) {
	frames := []codexrpc.Envelope{{ID: json.RawMessage("77"), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{}`)}}
	frames = append(frames, sessionPrefix()...)
	s, w, req := newSessionFixture(t, frames)
	if err := s.Stream(context.Background(), req, func(providers.Chunk) error { return nil }); err == nil {
		t.Fatal("unknown server request accepted")
	}
	writes := w.sent()
	if len(writes) != 1 || writes[0].Method != "initialize" {
		t.Fatalf("handshake continued after unknown server request: %+v", writes)
	}
}

func TestSessionNonFinalMessageCanPrecedeDelegation(t *testing.T) {
	for name, phase := range map[string]string{"omitted": "", "null": `,"phase":null`, "commentary": `,"phase":"commentary"`} {
		t.Run(name, func(t *testing.T) {
			frames := append(sessionPrefix(), sessionCommentary(phase)...)
			frames = append(frames, sessionTool()...)
			frames = append(frames, sessionToolCompletion())
			frames = append(frames, sessionFinal()...)
			s, _, req := newSessionFixture(t, frames)
			var out []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
				t.Fatal(err)
			}
			var proposal *providers.ToolCall
			var text string
			for _, c := range out {
				text += c.Text
				if c.ToolCall != nil {
					proposal = c.ToolCall
				}
			}
			if proposal == nil || text != "Delegating." {
				t.Fatalf("delegation after nonfinal message: %+v", out)
			}
			next := req
			next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", Content: text, ToolCalls: []providers.ToolCall{*proposal}}, providers.Message{Role: "tool", ToolCallID: proposal.ID, Content: `{"untrusted_output":"Done"}`})
			out = nil
			if err := s.Stream(context.Background(), next, collectSession(&out)); err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 || !out[len(out)-1].Done || out[len(out)-1].FinishReason != "stop" {
				t.Fatalf("resume: %+v", out)
			}
		})
	}
}

func TestSessionUsageIsIncrementalAcrossToolSegments(t *testing.T) {
	frames := sessionPrefix()
	frames = append(frames, sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":10,"outputTokens":2}}}`))
	frames = append(frames, sessionTool()...)
	frames = append(frames, sessionToolCompletion(), sessionNotice("thread/tokenUsage/updated", `{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":25,"outputTokens":5}}}`))
	frames = append(frames, sessionFinal()...)
	s, _, req := newSessionFixture(t, frames)
	var out []providers.Chunk
	if err := s.Stream(context.Background(), req, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	var proposal *providers.ToolCall
	var usage providers.Usage
	for _, c := range out {
		if c.ToolCall != nil {
			proposal = c.ToolCall
		}
		if c.Usage != nil {
			usage.InputTokens += c.Usage.InputTokens
			usage.OutputTokens += c.Usage.OutputTokens
		}
	}
	if proposal == nil || usage.InputTokens != 10 || usage.OutputTokens != 2 {
		t.Fatalf("first segment: %+v", out)
	}
	next := req
	next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{*proposal}}, providers.Message{Role: "tool", ToolCallID: proposal.ID, Content: `{"untrusted_output":"Done"}`})
	out = nil
	if err := s.Stream(context.Background(), next, collectSession(&out)); err != nil {
		t.Fatal(err)
	}
	usage = providers.Usage{}
	for _, c := range out {
		if c.Usage != nil {
			usage.InputTokens += c.Usage.InputTokens
			usage.OutputTokens += c.Usage.OutputTokens
		}
	}
	if usage.InputTokens != 15 || usage.OutputTokens != 3 {
		t.Fatalf("second segment double-counted cumulative usage: %+v", out)
	}
}
