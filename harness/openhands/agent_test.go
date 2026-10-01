package openhands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type agentTools struct {
	invoke func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error)
}

func (a agentTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("unscoped execution")
}
func (a agentTools) ExecuteScoped(c context.Context, e runtime.ToolExecution) (runtime.ToolResult, error) {
	return a.invoke(c, e)
}
func (a agentTools) ToolBehavior(string) runtime.ToolBehavior { return runtime.BehaviorReadOnly }
func agentSchema() []providers.Tool {
	return []providers.Tool{{Name: "nexus_lookup", Description: "Host lookup", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}}
}

func TestNativeOpenHandsAgentTask(t *testing.T) {
	python := os.Getenv("NEXUS_OPENHANDS_PYTHON")
	if python == "" {
		t.Skip("requires installed SDK")
	}
	for _, mode := range []string{"normal", "recoverable", "end", "denied", "wrong_model", "cancel", "cancel_tool", "turn_limit", "ollama"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			db, e := telemetry.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			c := AgentConfig{Config: runnerFixture(t, python), Tools: agentSchema(), MaxTurns: 3}
			if mode == "turn_limit" {
				c.MaxTurns = 1
			}
			c.Timeout = 20 * time.Second
			c.MaxOutputTokens = 1024
			if mode == "ollama" {
				c.UpstreamProtocol = "ollama"
			}
			var calls, effects, released atomic.Int32
			c.Admit = func(context.Context) (func(), error) { return func() { released.Add(1) }, nil }
			c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
				n := calls.Add(1)
				if mode == "cancel" {
					cancel()
					return nil, context.Canceled
				}
				request, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(request), "nexus_lookup") && !(n == 2 && mode == "end") {
					t.Error("missing host tool schema")
				}
				if n == 2 && !strings.Contains(string(request), "host result") {
					t.Error("missing host result")
				}
				model := "test-model"
				if mode == "wrong_model" {
					model = "wrong"
				}
				contentType := "text/event-stream"
				body := fmt.Sprintf("data: {\"id\":\"fixture-%d\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture-%d\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":4,\"total_tokens\":24}}\n\ndata: [DONE]\n\n", n, model, n, model)
				if n == 1 {
					body = fmt.Sprintf("data: {\"id\":\"fixture-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"one\",\"type\":\"function\",\"function\":{\"name\":\"nexus_lookup\",\"arguments\":\"{\\\"path\\\":\\\"fixture\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":4,\"total_tokens\":24}}\n\ndata: [DONE]\n\n", model, model)
				}
				if mode == "ollama" {
					contentType = "application/x-ndjson"
					body = `{"model":"test-model","message":{"role":"assistant","content":"answer"},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":4}` + "\n"
					if n == 1 {
						body = `{"model":"test-model","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"nexus_lookup","arguments":{"path":"fixture"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":4}` + "\n"
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			executor := agentTools{invoke: func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
				effects.Add(1)
				stored, err := db.Read(context.Background(), x.TaskID, 0, 100)
				if err != nil || len(stored) == 0 || stored[len(stored)-1].Kind != runtime.ToolStarted {
					t.Error("effect lacks durable start", err)
				}
				if mode == "cancel_tool" {
					cancel()
					return runtime.ToolResult{Effect: runtime.NoEffect}, context.Canceled
				}
				if x.Call.Name != "nexus_lookup" || string(x.Call.Arguments) != `{"path":"fixture"}` {
					t.Error("lost canonical call", x)
				}
				if mode == "denied" {
					return runtime.ToolResult{}, errors.New("host denied")
				}
				return runtime.ToolResult{Content: "host result", Effect: runtime.NoEffect, Recoverable: mode == "recoverable", Failed: mode == "recoverable", EndToolUse: mode == "end"}, nil
			}}
			task := Task{ID: "native-agent", SessionID: "session", Prompt: "Use the host lookup then answer", Class: harness.TaskClass{Domain: "fixture", Profile: "exact-v1", Difficulty: "easy"}, MaxOutputBytes: 65536}
			result, e := RunAgentTask(ctx, db, c, task, executor)
			events, readErr := db.Read(context.Background(), task.ID, 0, 100)
			if readErr != nil || len(events) == 0 || released.Load() != 1 {
				t.Fatal(readErr, e, released.Load())
			}
			last := events[len(events)-1]
			if mode == "wrong_model" || mode == "cancel" || mode == "cancel_tool" || mode == "turn_limit" || mode == "denied" {
				want := runtime.TaskFailed
				if mode == "cancel" || mode == "cancel_tool" {
					want = runtime.TaskCanceled
				}
				if e == nil || result.Result.Text != "" || result.Execution.Status != "" || last.Kind != want || last.Data.HarnessOutcome != nil || calls.Load() != 1 {
					t.Fatal(result, e, last, calls.Load())
				}
				if (mode == "wrong_model" || mode == "cancel") && effects.Load() != 0 {
					t.Fatal("effect before verification")
				}
				return
			}
			id, _ := c.Identity()
			if e != nil || result.Result.Text != "answer" || result.Result.Identity != id || result.Result.Usage != nil || result.Execution.Status != "completed" || calls.Load() != 2 || effects.Load() != 1 {
				t.Fatal(result, e, calls.Load(), effects.Load())
			}
			projection, e := runtime.ValidateHarnessAgentJournal(events, task.ID)
			if e != nil || projection == nil || projection.InputTokens != 40 || projection.OutputTokens != 8 {
				t.Fatal("canonical usage", projection, e)
			}
			if _, e = RunAgentTask(ctx, db, c, task, executor); e == nil || calls.Load() != 2 {
				t.Fatal("replayed completed inference")
			}
		})
	}
}
