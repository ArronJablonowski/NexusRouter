package runtime_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// TestHTTPProviderConformanceStopsBeforeContextOverflow binds each built-in
// HTTP adapter to the real durable loop. The first provider turn may propose a
// tool, but the paired result must be journaled and context-checked before a
// second provider handoff. An oversized next turn is therefore denied without
// another network request or loss of the completed tool evidence.
func TestHTTPProviderConformanceStopsBeforeContextOverflow(t *testing.T) {
	for _, kind := range []string{"openai_compatible", "ollama"} {
		t.Run(kind, func(t *testing.T) {
			dispatches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dispatches++
				if kind == "ollama" {
					fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"lookup","arguments":{"q":"darwin"}}}]},"done":false}`)
					fmt.Fprintln(w, `{"done":true,"done_reason":"tool_calls"}`)
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"darwin\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			provider, err := providers.NewHTTP(server.URL, kind, "", server.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			db, _ := store(t)
			estimates := 0
			loop := runtime.Loop{
				Journal:  db,
				Provider: provider,
				Tools: executor(func(_ context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
					if call.Name != "lookup" || string(call.Arguments) != `{"q":"darwin"}` {
						t.Fatalf("canonical tool call changed: %+v", call)
					}
					return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
				}),
				ContextEstimator: loopContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
					estimates++
					if len(request.Messages) == 1 {
						return 1, nil
					}
					if len(request.Messages) != 3 || len(request.Messages[1].ToolCalls) != 1 || request.Messages[2].ToolCallID != request.Messages[1].ToolCalls[0].ID {
						t.Fatalf("context estimate lost tool pair: %+v", request.Messages)
					}
					return 2049, nil
				}),
			}
			run := runRequest()
			run.ProviderID = kind
			run.MaxContextTokens = 2048
			_, err = loop.Run(context.Background(), run)
			events, readErr := db.Read(context.Background(), run.TaskID, 0, 100)
			if readErr != nil {
				t.Fatal(readErr)
			}
			assertContextBudgetFailure(t, events, err, 1)
			if dispatches != 1 || estimates != 2 {
				t.Fatalf("dispatches=%d estimates=%d", dispatches, estimates)
			}
			toolCompleted := false
			for _, event := range events {
				toolCompleted = toolCompleted || event.Kind == runtime.ToolCompleted
			}
			if !toolCompleted {
				t.Fatal("completed tool evidence was not durable before context denial")
			}
		})
	}
}
