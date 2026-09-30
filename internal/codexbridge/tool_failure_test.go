package codexbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestSessionFailedToolResponseAndBoundCompletion(t *testing.T) {
	for _, tc := range []struct {
		status   string
		success  bool
		accepted bool
	}{{"failed", false, true}, {"failed", true, false}, {"completed", false, false}, {"completed", true, false}} {
		t.Run(fmt.Sprint(tc.status, tc.success), func(t *testing.T) {
			frames := append(sessionPrefix(), sessionTool()...)
			frames = append(frames, sessionNotice("item/completed", fmt.Sprintf(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":%q,"success":%t}}`, tc.status, tc.success)))
			frames = append(frames, sessionFinal()...)
			s, w, req := newSessionFixture(t, frames)
			var chunks []providers.Chunk
			if err := s.Stream(context.Background(), req, collectSession(&chunks)); err != nil {
				t.Fatal(err)
			}
			if len(w.sent()) != 4 {
				t.Fatal("tool response sent before host result")
			}
			var call providers.ToolCall
			for _, chunk := range chunks {
				if chunk.ToolCall != nil {
					call = *chunk.ToolCall
				}
			}
			next := req
			next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, providers.Message{Role: "tool", ToolCallID: call.ID, Content: `{"error":"missing"}`, ToolFailed: true})
			chunks = nil
			err := s.Stream(context.Background(), next, collectSession(&chunks))
			if (err == nil) != tc.accepted {
				t.Fatal("completion trust mismatch", err)
			}
			sent := w.sent()
			if len(sent) != 5 {
				t.Fatal("missing one response", len(sent))
			}
			var response struct {
				Success      *bool `json:"success"`
				ContentItems []struct {
					Text string `json:"text"`
				} `json:"contentItems"`
			}
			if json.Unmarshal(sent[4].Result, &response) != nil || response.Success == nil || *response.Success || len(response.ContentItems) != 1 || response.ContentItems[0].Text != next.Messages[2].Content {
				t.Fatal(string(sent[4].Result))
			}
			if s.Stream(context.Background(), next, func(providers.Chunk) error { return nil }) == nil || len(w.sent()) != 5 {
				t.Fatal("response reused")
			}
		})
	}
}

func TestFailedToolRuntimeHandoffRequiresDurableCompletion(t *testing.T) {
	for _, rejectPersistence := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectPersistence), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "events.db")
			store, err := telemetry.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			frames := append(sessionPrefix(), sessionTool()...)
			frames = append(frames, sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"failed","success":false}}`))
			frames = append(frames, sessionFinal()...)
			s, wire, req := newSessionFixture(t, frames)
			const task = "failed-tool-runtime"
			persisted := false
			executions := 0
			loop := runtime.Loop{Provider: s, Journal: sessionRuntimeJournal(func(ctx context.Context, seq int64, event runtime.Event) error {
				if event.Kind == runtime.ToolCompleted {
					if len(wire.sent()) != 4 || event.Data.Code != "tool_failed" || event.Data.Effect != runtime.NoEffect {
						t.Fatal("invalid durable failure boundary", event, wire.sent())
					}
					if rejectPersistence {
						return errors.New("fixture persistence failure")
					}
				}
				if err := store.Append(ctx, seq, event); err != nil {
					return err
				}
				if event.Kind == runtime.ToolCompleted {
					persisted = true
				}
				return nil
			}), Tools: sessionRuntimeExecutor(func(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
				executions++
				events, err := store.Read(ctx, task, 0, 100)
				if err != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.ToolStarted || len(wire.sent()) != 4 {
					t.Fatal("tool before durable proposal", err)
				}
				return runtime.ToolResult{Content: `{"error":"fixture_missing"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
			})}
			result, runErr := loop.Run(ctx, runtime.RunRequest{TaskID: task, SessionID: "session", ProviderID: "codex", Inference: req, RequireText: true, MaxTurns: 3, MaxOutputBytes: 4096})
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			writes := wire.sent()
			if executions != 1 {
				t.Fatal("tool execution repeated", executions)
			}
			if rejectPersistence {
				if !errors.Is(runErr, runtime.ErrPersistence) || persisted || len(writes) != 4 {
					t.Fatal("uncommitted failure returned to Codex", runErr, persisted, writes)
				}
			} else {
				if runErr != nil || !persisted || result.Turns != 2 || result.Text != "Reviewed local result." || len(writes) != 5 {
					t.Fatal(result, runErr, persisted, writes)
				}
				var response struct {
					Success      *bool `json:"success"`
					ContentItems []struct {
						Text string `json:"text"`
					} `json:"contentItems"`
				}
				if json.Unmarshal(writes[4].Result, &response) != nil || response.Success == nil || *response.Success || len(response.ContentItems) != 1 || response.ContentItems[0].Text != `{"error":"fixture_missing"}` {
					t.Fatal(string(writes[4].Result))
				}
			}
			reader, err := telemetry.OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			events, err := reader.Read(ctx, task, 0, 100)
			if err != nil || len(events) == 0 {
				t.Fatal(err)
			}
			want := runtime.TaskCompleted
			if rejectPersistence {
				want = runtime.ToolStarted
			}
			if events[len(events)-1].Kind != want {
				t.Fatal("incorrect committed tail", events[len(events)-1])
			}
		})
	}
}

func TestImportedFailedToolHasExplicitFailureContent(t *testing.T) {
	messages := []providers.Message{{Role: "user", Content: "work"}, {Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "call", Content: "missing", ToolFailed: true}, {Role: "user", Content: "continue"}}
	items, last, err := initialHistory(messages)
	if err != nil || last != "continue" {
		t.Fatal(last, err)
	}
	var output struct{ Type, Output string }
	if json.Unmarshal(items[2], &output) != nil || output.Type != "function_call_output" || output.Output != "Tool execution failed.\nmissing" {
		t.Fatal(string(items[2]))
	}
	if messages[2].Content != "missing" || !messages[2].ToolFailed {
		t.Fatal("history mutated")
	}
}

func TestPendingBindsHistoricalFailureStatus(t *testing.T) {
	req := providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "user", Content: "work"}, {Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "old", Name: "delegate", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "old", Content: "missing", ToolFailed: true}}, Tools: []providers.Tool{{Name: "delegate", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	p, err := NewPending(req, "thread", "turn", "", CallRequest{ThreadID: "thread", TurnID: "turn", Namespace: "darwin", Tool: "delegate", CallID: "new", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	next := req
	next.Messages = append(append([]providers.Message(nil), req.Messages...), providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{p.Proposal()}}, providers.Message{Role: "tool", ToolCallID: "new", Content: "again", ToolFailed: true})
	next.Messages[2].ToolFailed = false
	if _, err = p.Resume(next); err == nil {
		t.Fatal("changed historical failure accepted")
	}
	next.Messages[2].ToolFailed = true
	if _, err = p.Resume(next); err != nil {
		t.Fatal("valid failure resume rejected", err)
	}
	next.Messages[4].ToolFailed = false
	if _, err = p.Resume(next); err == nil {
		t.Fatal("failed result reused as success")
	}
}
