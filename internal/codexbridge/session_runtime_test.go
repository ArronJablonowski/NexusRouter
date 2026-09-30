package codexbridge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type sessionRuntimeJournal func(context.Context, int64, runtime.Event) error

func (j sessionRuntimeJournal) Append(ctx context.Context, seq int64, event runtime.Event) error {
	return j(ctx, seq, event)
}

type sessionRuntimeExecutor func(context.Context, providers.ToolCall) (runtime.ToolResult, error)

func (e sessionRuntimeExecutor) Execute(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
	return e(ctx, call)
}

// This exercises the actual loop and SQLite journal, not a simulated caller
// directly manufacturing a continuation for the bridge. No process or network
// is started by the scripted wire.
func TestSessionRuntimeDurableToolBoundaries(t *testing.T) {
	for _, failure := range []runtime.Kind{"", runtime.ToolStarted, runtime.ToolCompleted} {
		name := string(failure)
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "events.db")
			store, err := telemetry.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			frames := append(sessionPrefix(), sessionTool()...)
			frames = append(frames, sessionNotice("item/completed", `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"dynamicToolCall","id":"call-1","tool":"delegate","namespace":"darwin","arguments":{"prompt":"Work","validation":"text"},"status":"completed","success":true}}`))
			frames = append(frames, sessionFinal()...)
			session, wire, inference := newSessionFixture(t, frames)
			const taskID = "codex-session-runtime-task"
			const content = `{"untrusted_output":"untrusted_result"}`
			executions := 0
			completedPersisted := false
			loop := runtime.Loop{
				Provider: session,
				Journal: sessionRuntimeJournal(func(ctx context.Context, seq int64, event runtime.Event) error {
					if event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted {
						if len(wire.sent()) != 4 {
							t.Fatal("tool response preceded durable tool result")
						}
						if event.Kind == failure {
							return errors.New("injected persistence failure")
						}
					}
					if err := store.Append(ctx, seq, event); err != nil {
						return err
					}
					if event.Kind == runtime.ToolCompleted {
						completedPersisted = true
					}
					return nil
				}),
				Tools: sessionRuntimeExecutor(func(ctx context.Context, call providers.ToolCall) (runtime.ToolResult, error) {
					executions++
					events, err := store.Read(ctx, taskID, 0, 100)
					if err != nil || len(events) == 0 || events[len(events)-1].Kind != runtime.ToolStarted {
						t.Fatalf("execution preceded durable intent: %v %+v", err, events)
					}
					if len(wire.sent()) != 4 || call.ID != "call-1" || call.Name != "delegate" {
						t.Fatalf("incorrect proposal boundary: %+v %+v", call, wire.sent())
					}
					return runtime.ToolResult{Content: content, Effect: runtime.NoEffect}, nil
				}),
			}
			result, runErr := loop.Run(ctx, runtime.RunRequest{
				TaskID: taskID, SessionID: "codex-runtime-session", ProviderID: "codex",
				Inference: inference, RequireText: true, MaxTurns: 3, MaxOutputBytes: 4096,
			})
			// The owner closes even when persistence prevents continuation. The
			// provider must not be left holding a live, unanswered tool request.
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-wire.closed:
			default:
				t.Fatal("owner cleanup did not close wire")
			}
			writes := wire.sent()
			if failure == "" {
				if runErr != nil || result.Text != "Reviewed local result." || result.Turns != 2 || executions != 1 || !completedPersisted {
					t.Fatalf("result %+v error %v executions %d", result, runErr, executions)
				}
				if len(writes) != 5 || string(writes[4].ID) != "50" || writes[4].Method != "" {
					t.Fatalf("incorrect continuation response: %+v", writes)
				}
				var response struct {
					Success      bool
					ContentItems []struct{ Type, Text string }
				}
				if json.Unmarshal(writes[4].Result, &response) != nil || !response.Success || len(response.ContentItems) != 1 || response.ContentItems[0].Type != "inputText" || response.ContentItems[0].Text != content {
					t.Fatalf("tool result changed: %s", writes[4].Result)
				}
			} else {
				wantExecutions := 0
				if failure == runtime.ToolCompleted {
					wantExecutions = 1
				}
				if !errors.Is(runErr, runtime.ErrPersistence) || executions != wantExecutions || completedPersisted || len(writes) != 4 {
					t.Fatalf("unsafe failed boundary: error %v executions %d writes %+v", runErr, executions, writes)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := telemetry.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			events, err := reopened.Read(ctx, taskID, 0, 100)
			if err != nil || len(events) == 0 {
				t.Fatalf("reopen: %v %+v", err, events)
			}
			wantLast := runtime.TaskCompleted
			if failure == runtime.ToolStarted {
				wantLast = runtime.TurnCompleted
			}
			if failure == runtime.ToolCompleted {
				wantLast = runtime.ToolStarted
			}
			if events[len(events)-1].Kind != wantLast {
				t.Fatalf("incorrect durable boundary: %+v", events)
			}
			for i, event := range events {
				if event.Sequence != int64(i+1) || event.TaskID != taskID {
					t.Fatalf("event identity/order: %+v", event)
				}
				if failure != "" && (event.Kind == runtime.TaskCompleted || event.Kind == runtime.ToolCompleted) {
					t.Fatalf("uncommitted success persisted: %+v", event)
				}
			}
		})
	}
}
