package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestDelegateRejectionPreservesChildToolUncertainty(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			ctx := context.Background()
			db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "uncertain.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			appendEvent := func(task string, seq int64, kind runtime.Kind, data runtime.Data) {
				t.Helper()
				e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-event-%d", task, seq), TaskID: task, SessionID: "session", CorrelationID: task, Sequence: seq, Time: time.Now().UTC(), Kind: kind, Data: data}
				if task == "execution" && seq > 1 {
					e.TurnID = "turn"
					e.AttemptID = "attempt"
				}
				if err := db.Append(ctx, seq-1, e); err != nil {
					t.Fatal(err)
				}
			}
			appendEvent("work", 1, runtime.TaskStarted, runtime.Data{ParentTaskID: "parent"})
			appendEvent("work", 2, runtime.TaskFailed, runtime.Data{Code: "worker_failed"})
			appendEvent("execution", 1, runtime.TaskStarted, runtime.Data{ParentTaskID: "work"})
			appendEvent("execution", 2, runtime.TurnStarted, runtime.Data{})
			appendEvent("execution", 3, runtime.TurnCompleted, runtime.Data{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "call", Name: "read_file", Arguments: json.RawMessage(`{"path":"file"}`)}}})
			appendEvent("execution", 4, runtime.ToolStarted, runtime.Data{ToolCallID: "call", ToolName: "read_file", Effect: runtime.UncertainEffect})
			if !pending {
				appendEvent("execution", 5, runtime.ToolCompleted, runtime.Data{ToolCallID: "call", ToolName: "read_file", Effect: runtime.UncertainEffect, Code: "tool_failed"})
				appendEvent("execution", 6, runtime.TaskFailed, runtime.Data{Code: "execution_failed"})
			}
			out := delegateRejection(ctx, db, "parent", "session", "work", "execution")
			if !out.Failed || out.Recoverable || out.Effect != runtime.UncertainEffect || out.Content != `{"error":"delegate_unavailable_or_rejected"}` {
				t.Fatal("uncertain child became a recoverable rejection", out)
			}
		})
	}
}

func TestDelegateRunnerPanicNeverBecomesRecoverable(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "panic.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := config.Defaults()
			cfg.Workers.Max = 2
			cfg.Workers.DelegateMaxCalls = 2
			registry := &tools.Registry{}
			var entered atomic.Int32
			if err := registerDelegate(registry, db, db, cfg, "parent", "session", "", true, func(context.Context, string, string, string, bool) (Result, error) {
				entered.Add(1)
				panic("private runner failure")
			}); err != nil {
				t.Fatal(err)
			}
			executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Allow}}}
			call := delegateSafetyCall(`{"prompt":"request","validation":"text"}`)
			want := int32(1)
			if batch {
				call = batchCall(batchTwo)
				want = 2
			}
			out, err := executor.Execute(ctx, call)
			if err == nil || out.Effect != runtime.UncertainEffect || out.Recoverable || out.Content != "" || entered.Load() != want {
				t.Fatal("swallowed panic became repairable", out, err, entered.Load())
			}
		})
	}
}
