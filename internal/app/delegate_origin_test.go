package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// Unit fixtures model the same scoped invocation used by runtime.Loop.
type scopedDelegateTestExecutor struct{ tools.Executor }

func (e scopedDelegateTestExecutor) Execute(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
	return e.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "parent", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: call})
}

func TestDelegationOriginPersistsBeforeSingleAndBatchExecution(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "origin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := &tools.Registry{}
	cfg := config.Defaults()
	cfg.Workers.DelegateMaxCalls = 3
	var mu sync.Mutex
	seen := map[string]runtime.DelegationOrigin{}
	if err := registerDelegate(registry, db, db, cfg, "parent", "session", "", true, func(ctx context.Context, prompt, validation, work string, local bool) (Result, error) {
		page, err := db.ReadEventPage(ctx, work, 0, 100)
		if err != nil || len(page.Events) < 1 {
			t.Error("origin unavailable before execution", err)
			return Result{}, ErrAdmission
		}
		start := page.Events[0]
		o := start.Data.DelegationOrigin
		if o == nil || o.Validate() != nil || start.Data.ParentTaskID != "parent" || start.SessionID != "session" {
			t.Error("missing durable origin")
			return Result{}, ErrAdmission
		}
		mu.Lock()
		seen[prompt] = *o.Clone()
		mu.Unlock()
		return Result{TaskID: "execution-" + work, Text: prompt}, nil
	}, nil, applicationToolPolicy()); err != nil {
		t.Fatal(err)
	}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy()}
	for _, call := range []providers.ToolCall{
		{ID: "single-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"single","validation":"text"}`)},
		{ID: "batch-call", Name: "delegate_batch", Arguments: json.RawMessage(batchTwo)},
	} {
		// The batch's second fixture validates Go; use text here so acceptance
		// remains independent of this origin-provenance test.
		if call.Name == "delegate_batch" {
			call.Arguments = json.RawMessage(`{"tasks":[{"prompt":"first","validation":"text"},{"prompt":"second","validation":"text"}]}`)
		}
		if _, err := executor.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "parent", SessionID: "session", TurnID: "actual-turn", AttemptID: "actual-attempt", Call: call}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 {
		t.Fatal("missing executions", len(seen))
	}
	for prompt, o := range seen {
		if o.TurnID != "actual-turn" || o.AttemptID != "actual-attempt" {
			t.Fatal("model identity lost", o)
		}
		if prompt == "single" {
			if o.ToolCallID != "single-call" || o.ToolName != "delegate" || o.BatchIndex != nil {
				t.Fatal("single origin", o)
			}
		} else {
			want := 0
			if prompt == "second" {
				want = 1
			}
			if o.ToolCallID != "batch-call" || o.ToolName != "delegate_batch" || o.BatchIndex == nil || *o.BatchIndex != want {
				t.Fatal("batch position mixed", o)
			}
		}
	}
}

func TestDelegationRejectsUnscopedAndForeignInvocation(t *testing.T) {
	for _, mode := range []string{"unscoped", "task", "session"} {
		t.Run(mode, func(t *testing.T) {
			db, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "denied.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			journal := &delegateFailJournal{}
			called := false
			executor := delegateSafetyExecutor(t, db, journal, func(context.Context, string, string, string, bool) (Result, error) {
				called = true
				return Result{}, nil
			})
			call := delegateSafetyCall(`{"prompt":"request","validation":"text"}`)
			if mode == "unscoped" {
				_, err = executor.Executor.Execute(context.Background(), call)
			} else {
				x := runtime.ToolExecution{TaskID: "parent", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: call}
				if mode == "task" {
					x.TaskID = "other"
				} else {
					x.SessionID = "other"
				}
				_, err = executor.ExecuteScoped(context.Background(), x)
			}
			if err != nil || called || journal.calls != 0 {
				t.Fatal("unbound work dispatched", err, called, journal.calls)
			}
		})
	}
}
