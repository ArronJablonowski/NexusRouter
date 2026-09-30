package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// These are protocol fixtures, not a claim of live Sol/Ollama qualification.
func TestHybridSolCoordinatorDelegatesToIsolatedOllama(t *testing.T) {
	for _, scenario := range []string{"success", "worker_empty", "local_only", "private_request"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const token = "hybrid-fixture-credential"
			const task = "Write a Go function returning 42."
			const answer = "package answer\nfunc Answer() int { return 42 }"
			var parentCalls, childCalls atomic.Int32
			toolResults := make(chan string, 1)
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				childCalls.Add(1)
				var body struct {
					Model    string              `json:"model"`
					Messages []providers.Message `json:"messages"`
					Tools    []json.RawMessage   `json:"tools"`
				}
				if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("local worker request or credential isolation invalid")
					return
				}
				if body.Model != "qwen2.5-coder:7b" || len(body.Tools) != 0 || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != task {
					t.Error("worker received unexpected model, tools, or parent context")
				}
				content := answer
				if scenario == "worker_empty" {
					content = ""
				}
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", content)
			}))
			defer child.Close()
			parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := parentCalls.Add(1)
				var body struct {
					Model    string              `json:"model"`
					Stream   bool                `json:"stream"`
					Messages []providers.Message `json:"messages"`
					Tools    []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer "+token || json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("coordinator request/auth invalid")
					return
				}
				if body.Model != "gpt-5.6-sol" || !body.Stream {
					t.Error("wrong coordinator model or streaming mode")
				}
				delegateFound := false
				for _, tool := range body.Tools {
					if tool.Function.Name == "delegate" {
						delegateFound = true
					}
					if tool.Function.Name == "read_file" {
						t.Error("unexpected filesystem capability")
					}
				}
				if !delegateFound {
					t.Error("missing delegation capability")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if call == 1 {
					args, _ := json.Marshal(map[string]string{"prompt": task, "validation": "go_source"})
					fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"worker-call\",\"type\":\"function\",\"function\":{\"name\":\"delegate\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", string(args))
					return
				}
				for _, message := range body.Messages {
					if message.Role == "tool" {
						toolResults <- message.Content
					}
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Coordinator reviewed the worker result.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer parent.Close()
			cfg := config.Defaults()
			cfg.Mode = "hybrid"
			if scenario == "local_only" {
				cfg.Mode = "local_only"
			}
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "hybrid.db")
			cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 2, 1
			cfg.Workers.DelegateModel = "worker"
			cfg.Workers.DelegateReadTools = false
			cfg.Providers = []config.Provider{
				{ID: "cloud", Kind: "openai_compatible", Endpoint: parent.URL, APIKeyEnv: "HYBRID_FIXTURE_KEY"},
				{ID: "local", Kind: "ollama", Endpoint: child.URL},
			}
			zero := 0.0
			cfg.Models = []config.Model{
				{ID: "coordinator", Model: "gpt-5.6-sol", Provider: "cloud", Locality: "cloud", ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}},
				{ID: "worker", Model: "qwen2.5-coder:7b", Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}},
			}
			svc, err := NewService(cfg, func(name string) string {
				if name == "HYBRID_FIXTURE_KEY" {
					return token
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			result, err := svc.Run(ctx, Request{ModelID: "coordinator", Prompt: "Delegate the implementation, then review it. Parent-only context.", LocalRequired: scenario == "private_request"})
			if scenario == "local_only" || scenario == "private_request" {
				if err == nil || parentCalls.Load() != 0 || childCalls.Load() != 0 {
					t.Fatal("cloud admission bypassed privacy", err, parentCalls.Load(), childCalls.Load())
				}
				return
			}
			if err != nil || result.Text != "Coordinator reviewed the worker result." || parentCalls.Load() != 2 || childCalls.Load() != 1 {
				t.Fatal(result, err, parentCalls.Load(), childCalls.Load())
			}
			var toolResult string
			select {
			case toolResult = <-toolResults:
			default:
				t.Fatal("missing worker result")
			}
			if scenario == "worker_empty" {
				if !validDelegateRejection(toolResult) {
					t.Fatal("empty worker output reported as accepted", toolResult)
				}
				return
			}
			var envelope struct {
				Work      string `json:"work_task_id"`
				Execution string `json:"execution_task_id"`
				Output    string `json:"untrusted_output"`
			}
			if json.Unmarshal([]byte(toolResult), &envelope) != nil || envelope.Output != answer || envelope.Work == "" || envelope.Execution == "" || strings.Contains(toolResult, token) {
				t.Fatal("invalid untrusted worker envelope", toolResult)
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			parentSnapshot, err := db.TaskSnapshot(ctx, result.TaskID)
			if err != nil || parentSnapshot.State != "completed" || parentSnapshot.Privacy != "cloud_allowed" {
				t.Fatal(parentSnapshot, err)
			}
			work, err := db.TaskSnapshot(ctx, envelope.Work)
			if err != nil || work.State != "completed" || work.ParentTaskID != result.TaskID {
				t.Fatal(work, err)
			}
			execution, err := db.TaskSnapshot(ctx, envelope.Execution)
			if err != nil || execution.State != "completed" || execution.ParentTaskID != envelope.Work || execution.Privacy != "local_only" {
				t.Fatal(execution, err)
			}
		})
	}
}
