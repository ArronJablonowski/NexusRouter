package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestDelegateBatchParallelOrderedDurableAndAtomicCallLimit(t *testing.T) {
	for _, limit := range []int{4, 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var parents, children atomic.Int32
			both := make(chan struct{})
			output := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string              `json:"model"`
					Messages []providers.Message `json:"messages"`
					Tools    []json.RawMessage   `json:"tools"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad body")
					return
				}
				if body.Model == "child" {
					if children.Add(1) == 2 {
						close(both)
					}
					if len(body.Tools) != 0 || len(body.Messages) != 1 || body.Messages[0].Role != "user" {
						t.Error("child isolation failed", body)
					}
					select {
					case <-both:
					case <-time.After(5 * time.Second):
						t.Error("children did not run concurrently")
						return
					case <-r.Context().Done():
						return
					}
					fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"done_reason":"stop"}`, body.Messages[0].Content+" answer")
					return
				}
				if body.Model != "parent" {
					t.Error("unexpected model")
					return
				}
				if parents.Add(1) == 1 {
					fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate_batch","arguments":{"tasks":[{"prompt":"first","validation":"text"},{"prompt":"second","validation":"text"}]}}}]},"done":true,"done_reason":"tool_calls"}`)
					return
				}
				for _, m := range body.Messages {
					if m.Role == "tool" {
						output <- m.Content
					}
				}
				fmt.Fprintln(w, `{"message":{"content":"parent final"},"done":true,"done_reason":"stop"}`)
			}))
			defer func() { cancel(); server.Close() }()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Workers.Max = 3
			cfg.Hardware.Concurrent = "3"
			cfg.Workers.DelegateModel = "child"
			cfg.Workers.DelegateMaxCalls = limit
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "batch.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			for _, id := range []string{"parent", "child"} {
				cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			parent, err := svc.Run(ctx, Request{ModelID: "parent", Prompt: "parallel tasks"})
			if err != nil || parent.Text != "parent final" || parents.Load() != 2 {
				t.Fatal(parent, err, parents.Load())
			}
			var content string
			select {
			case content = <-output:
			default:
				t.Fatal("missing batch output")
			}
			if limit == 1 {
				if children.Load() != 0 || !validDelegateRejection(content) {
					t.Fatal("partial batch admission", children.Load(), content)
				}
				return
			}
			if children.Load() != 2 {
				t.Fatal(children.Load())
			}
			var envelope struct {
				Results []struct {
					Work      string `json:"work_task_id"`
					Execution string `json:"execution_task_id"`
					Output    string `json:"untrusted_output"`
				} `json:"results"`
			}
			if json.Unmarshal([]byte(content), &envelope) != nil || len(envelope.Results) != 2 {
				t.Fatal(content)
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			seen := map[string]bool{}
			for i, result := range envelope.Results {
				want := []string{"first answer", "second answer"}[i]
				if result.Output != want || result.Work == "" || result.Execution == "" || seen[result.Work] || seen[result.Execution] || result.Work == result.Execution {
					t.Fatal("order or identity invalid", content)
				}
				seen[result.Work], seen[result.Execution] = true, true
				work, err := db.TaskSnapshot(ctx, result.Work)
				if err != nil || work.State != "completed" || work.ParentTaskID != parent.TaskID {
					t.Fatal(work, err)
				}
				child, err := db.TaskSnapshot(ctx, result.Execution)
				if err != nil || child.State != "completed" || child.ParentTaskID != result.Work || child.Messages[len(child.Messages)-1].Content != want {
					t.Fatal(child, err)
				}
				facts, err := db.Read(ctx, result.Work, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				complete := false
				for _, e := range facts {
					if e.Kind == runtime.WorkerCompleted {
						complete = true
					}
				}
				if !complete {
					t.Fatal("worker acceptance not durable", facts)
				}
			}
		})
	}
}
