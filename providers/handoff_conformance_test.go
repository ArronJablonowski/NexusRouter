package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestHTTPAdaptersPreserveCanonicalToolHandoffs exercises the provider-neutral
// conversation at the transport boundary in both directions. A call emitted by
// one adapter is deliberately submitted to the other adapter after a host tool
// result; adapter-specific wire differences must not leak into the canonical
// history or break the call/result pair.
func TestHTTPAdaptersPreserveCanonicalToolHandoffs(t *testing.T) {
	tests := []struct {
		name       string
		firstKind  string
		secondKind string
		firstBody  string
		secondBody string
	}{
		{
			name:       "openai_to_ollama",
			firstKind:  "openai_compatible",
			secondKind: "ollama",
			firstBody: "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"handoff-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"darwin\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
			secondBody: "{\"message\":{\"content\":\"accepted\"},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":7,\"eval_count\":1}\n",
		},
		{
			name:       "ollama_to_openai",
			firstKind:  "ollama",
			secondKind: "openai_compatible",
			firstBody:  "{\"message\":{\"tool_calls\":[{\"function\":{\"name\":\"lookup\",\"arguments\":{\"q\":\"darwin\"}}}]},\"done\":false}\n{\"done\":true,\"done_reason\":\"tool_calls\"}\n",
			secondBody: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"accepted\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1,\"total_tokens\":8}}\n\n" +
				"data: [DONE]\n\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := fixtureProvider(t, tc.firstKind, func(w http.ResponseWriter, r *http.Request) {
				assertConformancePath(t, tc.firstKind, r.URL.Path)
				fmt.Fprint(w, tc.firstBody)
			})
			var call *ToolCall
			var firstDone bool
			if err := first.Stream(context.Background(), request(), func(chunk Chunk) error {
				if chunk.ToolCall != nil {
					copy := *chunk.ToolCall
					copy.Arguments = append(json.RawMessage(nil), chunk.ToolCall.Arguments...)
					call = &copy
				}
				firstDone = firstDone || chunk.Done
				return nil
			}); err != nil || call == nil || !firstDone {
				t.Fatalf("first adapter did not produce a complete call: call=%+v done=%v err=%v", call, firstDone, err)
			}

			second := fixtureProvider(t, tc.secondKind, func(w http.ResponseWriter, r *http.Request) {
				assertConformancePath(t, tc.secondKind, r.URL.Path)
				if err := handoffWireError(tc.secondKind, r, *call); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, tc.secondBody)
			})
			handoff := request()
			handoff.Messages = append(handoff.Messages,
				Message{Role: "assistant", ToolCalls: []ToolCall{*call}},
				Message{Role: "tool", ToolCallID: call.ID, Content: `{"value":"evidence"}`},
			)
			var text string
			var usage *Usage
			var secondDone bool
			if err := second.Stream(context.Background(), handoff, func(chunk Chunk) error {
				text += chunk.Text
				if chunk.Usage != nil {
					copy := *chunk.Usage
					usage = &copy
				}
				secondDone = secondDone || chunk.Done
				return nil
			}); err != nil || text != "accepted" || usage == nil || *usage != (Usage{InputTokens: 7, OutputTokens: 1}) || !secondDone {
				t.Fatalf("second adapter rejected handoff: text=%q usage=%+v done=%v err=%v", text, usage, secondDone, err)
			}
		})
	}
}

func TestHTTPAdapterCancellationConformance(t *testing.T) {
	for _, kind := range []string{"openai_compatible", "ollama"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{})
			provider := fixtureProvider(t, kind, func(w http.ResponseWriter, r *http.Request) {
				if kind == "ollama" {
					fmt.Fprintln(w, `{"message":{"content":"partial"},"done":false}`)
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
				}
				w.(http.Flusher).Flush()
				close(started)
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
					t.Error("adapter cancellation did not reach transport")
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks := 0
			err := provider.Stream(ctx, request(), func(chunk Chunk) error {
				if chunk.Text != "partial" || chunk.Done || chunk.ToolCall != nil {
					t.Fatalf("unexpected pre-cancel chunk: %+v", chunk)
				}
				chunks++
				cancel()
				return nil
			})
			<-started
			if err != context.Canceled || chunks != 1 {
				t.Fatalf("cancellation result chunks=%d err=%v", chunks, err)
			}
		})
	}
}

func assertConformancePath(t *testing.T, kind, path string) {
	t.Helper()
	want := "/chat/completions"
	if kind == "ollama" {
		want = "/api/chat"
	}
	if path != want {
		t.Errorf("%s path = %q, want %q", kind, path, want)
	}
}

func handoffWireError(kind string, r *http.Request, call ToolCall) error {
	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return err
	}
	if len(body.Messages) != 3 || len(body.Messages[1].ToolCalls) != 1 {
		return fmt.Errorf("handoff messages = %+v", body.Messages)
	}
	wireCall := body.Messages[1].ToolCalls[0]
	if body.Messages[1].Role != "assistant" || wireCall.ID != call.ID || wireCall.Function.Name != call.Name {
		return fmt.Errorf("call identity changed: %+v", wireCall)
	}
	if body.Messages[2].Role != "tool" || body.Messages[2].Content != `{"value":"evidence"}` {
		return fmt.Errorf("tool result changed: %+v", body.Messages[2])
	}
	if kind == "ollama" {
		if body.Messages[2].ToolName != call.Name || body.Messages[2].ToolCallID != "" || string(wireCall.Function.Arguments) != string(call.Arguments) {
			return fmt.Errorf("invalid Ollama handoff wire: call=%+v result=%+v", wireCall, body.Messages[2])
		}
		return nil
	}
	var arguments string
	if json.Unmarshal(wireCall.Function.Arguments, &arguments) != nil || arguments != string(call.Arguments) || body.Messages[2].ToolCallID != call.ID || body.Messages[2].ToolName != "" {
		return fmt.Errorf("invalid OpenAI handoff wire: call=%+v result=%+v", wireCall, body.Messages[2])
	}
	return nil
}
