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
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestDelegateRealLoopIsolationValidationAndCapacity(t *testing.T) {
	for _, mode := range []string{"success", "submitted", "capacity", "empty", "invalid_go"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var parents, children atomic.Int32
			toolOutput := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string              `json:"model"`
					Messages []providers.Message `json:"messages"`
					Tools    []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid body")
					return
				}
				if body.Model == "child" {
					children.Add(1)
					if len(body.Tools) != 0 || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "isolated child prompt" {
						t.Error("child received ambient context or capabilities", body)
					}
					text := "validated child answer"
					if mode == "empty" {
						text = " "
					}
					if mode == "invalid_go" {
						text = "private invalid Go output"
					}
					fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"done_reason":"stop"}`, text)
					return
				}
				if body.Model != "parent" {
					t.Error("unexpected model", body.Model)
					return
				}
				if parents.Add(1) == 1 {
					found := false
					for _, tool := range body.Tools {
						if tool.Function.Name == "delegate" {
							found = true
						}
					}
					if !found {
						t.Error("delegate tool not exposed")
					}
					validation := "text"
					if mode == "invalid_go" {
						validation = "go_source"
					}
					fmt.Fprintf(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"isolated child prompt","validation":%q}}}]},"done":true,"done_reason":"tool_calls"}`, validation)
					return
				}
				found := ""
				for _, message := range body.Messages {
					if message.Role == "tool" {
						found = message.Content
					}
				}
				toolOutput <- found
				fmt.Fprintln(w, `{"message":{"content":"parent final"},"done":true,"done_reason":"stop"}`)
			}))
			defer func() { cancel(); server.Close() }()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Workers.Max = 2
			cfg.Hardware.Concurrent = "2"
			if mode == "capacity" {
				cfg.Workers.Max = 1
			}
			cfg.Workers.DelegateModel = "child"
			cfg.Workers.DelegateMaxCalls = 1
			cfg.Workers.DelegateMaxCost = 0
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "delegate.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			for _, id := range []string{"parent", "child"} {
				cfg.Models = append(cfg.Models, config.Model{ID: id, Provider: "local", Model: id, Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			request := Request{ModelID: "parent", Prompt: "ambient parent secret"}
			submissionID := ""
			if mode == "submitted" {
				status, err := svc.Submit(ctx, "delegate-submission-key", request)
				if err != nil {
					t.Fatal(err)
				}
				writer, err := telemetry.Open(ctx, cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := writer.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
				writer.Close()
				if err != nil || claim.Status.ID != status.ID {
					t.Fatal(claim.Status, err)
				}
				submissionID = status.ID
				request.submissionID, request.submissionToken = status.ID, claim.Token
			}
			result, err := svc.Run(ctx, request)
			if err != nil || result.Text != "parent final" || parents.Load() != 2 {
				t.Fatal(result, err, parents.Load())
			}
			var output string
			select {
			case output = <-toolOutput:
			default:
				t.Fatal("tool output missing")
			}
			wantChildren := int32(1)
			if mode == "capacity" {
				wantChildren = 0
			}
			if children.Load() != wantChildren {
				t.Fatal("unexpected child dispatch", children.Load())
			}
			if mode != "success" && mode != "submitted" {
				if !validDelegateRejection(output) || strings.Contains(output, "private") {
					t.Fatal("invalid child output escaped", output)
				}
				return
			}
			var envelope struct {
				WorkID      string `json:"work_task_id"`
				ExecutionID string `json:"execution_task_id"`
				Output      string `json:"untrusted_output"`
			}
			if json.Unmarshal([]byte(output), &envelope) != nil || envelope.WorkID == "" || envelope.ExecutionID == "" || envelope.Output != "validated child answer" {
				t.Fatal(output)
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			work, err := db.TaskSnapshot(ctx, envelope.WorkID)
			if err != nil || work.State != "completed" || work.ParentTaskID != result.TaskID {
				t.Fatal(work, err)
			}
			child, err := db.TaskSnapshot(ctx, envelope.ExecutionID)
			if err != nil || child.State != "completed" || child.ParentTaskID != envelope.WorkID {
				t.Fatal(child, err)
			}
			events, err := db.Read(ctx, envelope.WorkID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			started, completed := false, false
			for _, e := range events {
				started = started || e.Kind == runtime.WorkerStarted
				completed = completed || e.Kind == runtime.WorkerCompleted
			}
			if !started || !completed {
				t.Fatal("missing durable worker lifecycle", events)
			}
			inspected, err := InspectTask(ctx, cfg.Telemetry.Database, envelope.WorkID)
			if err != nil || inspected.State != "completed" {
				t.Fatal(inspected, err)
			}
			page, err := ReadTaskEvents(ctx, cfg.Telemetry.Database, envelope.WorkID, 0, 100)
			if err != nil || page.Validate() != nil || len(page.Events) == 0 {
				t.Fatal(page, err)
			}
			for _, event := range page.Events {
				if event.CorrelationID != envelope.WorkID {
					t.Fatal("worker correlation is not self-attributed", event)
				}
			}
			if mode == "submitted" {
				status, err := svc.SubmissionStatus(ctx, submissionID)
				if err != nil || len(status.TaskIDs) != 3 {
					t.Fatal(status, err)
				}
				linked := map[string]bool{}
				for _, id := range status.TaskIDs {
					linked[id] = true
				}
				for _, id := range []string{result.TaskID, envelope.WorkID, envelope.ExecutionID} {
					if !linked[id] {
						t.Fatal("submission omitted descendant", status.TaskIDs, id)
					}
					facts, err := db.Read(ctx, id, 0, 100)
					if err != nil || len(facts) == 0 || facts[0].Kind != runtime.TaskStarted || facts[0].Data.SubmissionID != submissionID {
						t.Fatal("unbound start", facts, err)
					}
				}
			}
		})
	}
}
