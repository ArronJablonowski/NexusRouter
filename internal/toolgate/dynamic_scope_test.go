package toolgate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestDynamicScopeOwnsExactWriterAndSpendsIdempotentCallAuthority(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	arguments := json.RawMessage(`{"board_id":"board_a","idempotency_key":"operation-key-0001"}`)
	call := providers.ToolCall{ID: "call", Name: "mutate_board", Arguments: arguments}
	now := time.Now().UTC()
	for index, event := range []runtime.Event{
		{Version: 1, ID: "task-start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted},
		{Version: 1, ID: "turn-start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 2, Time: now.Add(time.Millisecond), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"},
		{Version: 1, ID: "turn-complete", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 3, Time: now.Add(2 * time.Millisecond), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{call}}},
		{Version: 1, ID: "tool-start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 4, Time: now.Add(3 * time.Millisecond), Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: runtime.BehaviorIdempotentWrite, Effect: runtime.UncertainEffect}},
	} {
		if err = store.Append(ctx, int64(index), event); err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := tools.IdentifierScope("workboard", "board_id")
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	handlerCalls := 0
	if err = registry.Register(tools.Definition{Tool: providers.Tool{Name: call.Name, Description: "Mutate one board",
		Parameters: json.RawMessage(`{"type":"object","properties":{"board_id":{"type":"string"},"idempotency_key":{"type":"string","minLength":16}},"required":["board_id","idempotency_key"],"additionalProperties":false}`)},
		Scope: "workboard", ResolveScope: resolver, Behavior: runtime.BehaviorIdempotentWrite,
		Handler: func(handlerCtx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
			handlerCalls++
			leases, inspectErr := store.InspectLeases(handlerCtx, "workboard:board_a")
			if inspectErr != nil || len(leases) != 1 || !leases[0].Writer || leases[0].Released || leases[0].TaskID != "task" {
				t.Fatalf("handler lacks exact board writer: leases=%+v err=%v", leases, inspectErr)
			}
			if sibling, inspectErr := store.InspectLeases(handlerCtx, "workboard:board_b"); inspectErr != nil || len(sibling) != 0 {
				t.Fatalf("writer escaped exact board scope: leases=%+v err=%v", sibling, inspectErr)
			}
			return runtime.ToolResult{Content: `{"outcome":"committed"}`, Effect: runtime.ConfirmedEffect}, nil
		}}); err != nil {
		t.Fatal(err)
	}
	var approved approvals.Request
	gate := &Gate{Store: store, Review: func(_ context.Context, request approvals.Request) (string, bool, error) {
		approved = request
		return "operator", true, nil
	}}
	executor := tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Ask}, Authority: gate}
	execution := runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: call}
	out, err := executor.ExecuteScoped(ctx, execution)
	if err != nil || out.Effect != runtime.ConfirmedEffect || handlerCalls != 1 || approved.Scope != "workboard:board_a" ||
		approved.ToolBehavior != runtime.BehaviorIdempotentWrite {
		t.Fatalf("out=%+v approval=%+v calls=%d err=%v", out, approved, handlerCalls, err)
	}
	record, err := store.ReadApproval(ctx, approved.ID)
	if err != nil || record.State != approvals.Consumed || !record.Request.Matches(approved) {
		t.Fatalf("approval was not durably consumed: record=%+v err=%v", record, err)
	}
	if leases, inspectErr := store.InspectLeases(ctx, approved.Scope); inspectErr != nil || len(leases) != 0 {
		t.Fatalf("writer remained after joined handler: leases=%+v err=%v", leases, inspectErr)
	}
	// BehaviorIdempotentWrite describes the domain operation; it never permits
	// the execution layer to replay a spent model tool call.
	out, err = executor.ExecuteScoped(ctx, execution)
	if !errors.Is(err, tools.ErrDenied) || out.Effect != runtime.NoEffect || handlerCalls != 1 {
		t.Fatalf("spent call authority replayed: out=%+v calls=%d err=%v", out, handlerCalls, err)
	}
}

func TestDynamicWorkboardReadBlocksSameBoardWriterButNotSibling(t *testing.T) {
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resolver, err := tools.IdentifierScope("workboard", "board_id")
	if err != nil {
		t.Fatal(err)
	}
	readEntered, releaseRead := make(chan struct{}), make(chan struct{})
	registry := &tools.Registry{}
	if err = registry.Register(tools.Definition{Tool: providers.Tool{Name: "read_board", Parameters: json.RawMessage(`{"type":"object","properties":{"board_id":{"type":"string"}},"required":["board_id"],"additionalProperties":false}`)},
		Scope: "workboard", ResolveScope: resolver, ReadOnly: true, Behavior: runtime.BehaviorReadOnly,
		Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			close(readEntered)
			<-releaseRead
			return runtime.ToolResult{Effect: runtime.NoEffect}, nil
		}}); err != nil {
		t.Fatal(err)
	}
	if err = registry.Register(tools.Definition{Tool: providers.Tool{Name: "mutate_board", Parameters: json.RawMessage(`{"type":"object","properties":{"board_id":{"type":"string"},"idempotency_key":{"type":"string","minLength":16}},"required":["board_id","idempotency_key"],"additionalProperties":false}`)},
		Scope: "workboard", ResolveScope: resolver, Behavior: runtime.BehaviorIdempotentWrite,
		Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
		}}); err != nil {
		t.Fatal(err)
	}
	appendPending := func(taskID string, call providers.ToolCall, behavior runtime.ToolBehavior) {
		t.Helper()
		now := time.Now().UTC()
		for index, event := range []runtime.Event{
			{Version: 1, ID: taskID + "-start", TaskID: taskID, SessionID: taskID + "-session", CorrelationID: taskID, Sequence: 1, Time: now, Kind: runtime.TaskStarted},
			{Version: 1, ID: taskID + "-turn", TaskID: taskID, SessionID: taskID + "-session", CorrelationID: taskID, Sequence: 2, Time: now.Add(time.Millisecond), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"},
			{Version: 1, ID: taskID + "-complete", TaskID: taskID, SessionID: taskID + "-session", CorrelationID: taskID, Sequence: 3, Time: now.Add(2 * time.Millisecond), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{call}}},
			{Version: 1, ID: taskID + "-tool", TaskID: taskID, SessionID: taskID + "-session", CorrelationID: taskID, Sequence: 4, Time: now.Add(3 * time.Millisecond), Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: behavior, Effect: runtime.UncertainEffect}},
		} {
			if appendErr := store.Append(ctx, int64(index), event); appendErr != nil {
				t.Fatal(appendErr)
			}
		}
	}
	readCall := providers.ToolCall{ID: "read-call", Name: "read_board", Arguments: json.RawMessage(`{"board_id":"board_a"}`)}
	writerACall := providers.ToolCall{ID: "writer-a-call", Name: "mutate_board", Arguments: json.RawMessage(`{"board_id":"board_a","idempotency_key":"operation-key-0001"}`)}
	writerBCall := providers.ToolCall{ID: "writer-b-call", Name: "mutate_board", Arguments: json.RawMessage(`{"board_id":"board_b","idempotency_key":"operation-key-0002"}`)}
	appendPending("read-task", readCall, runtime.BehaviorReadOnly)
	appendPending("writer-a-task", writerACall, runtime.BehaviorIdempotentWrite)
	appendPending("writer-b-task", writerBCall, runtime.BehaviorIdempotentWrite)
	gate := &Gate{Store: store, Review: func(context.Context, approvals.Request) (string, bool, error) { return "operator", true, nil }}
	readExecutor := tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{{Tool: "read_board", Scope: "*", Decision: tools.Allow}}}, Reader: gate}
	writeExecutor := tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{{Tool: "mutate_board", Scope: "*", Decision: tools.Ask}}}, Authority: gate}
	type executionResult struct {
		out runtime.ToolResult
		err error
	}
	readDone := make(chan executionResult, 1)
	go func() {
		out, runErr := readExecutor.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "read-task", SessionID: "read-task-session", TurnID: "turn", AttemptID: "attempt", Call: readCall})
		readDone <- executionResult{out: out, err: runErr}
	}()
	select {
	case <-readEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not acquire its board scope")
	}
	if out, writeErr := writeExecutor.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "writer-a-task", SessionID: "writer-a-task-session", TurnID: "turn", AttemptID: "attempt", Call: writerACall}); !errors.Is(writeErr, tools.ErrDenied) || out.Effect != runtime.NoEffect {
		t.Fatalf("same-board writer overlapped reader: out=%+v err=%v", out, writeErr)
	}
	if out, writeErr := writeExecutor.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "writer-b-task", SessionID: "writer-b-task-session", TurnID: "turn", AttemptID: "attempt", Call: writerBCall}); writeErr != nil || out.Effect != runtime.ConfirmedEffect {
		t.Fatalf("sibling-board writer was blocked: out=%+v err=%v", out, writeErr)
	}
	close(releaseRead)
	var read executionResult
	select {
	case read = <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not join")
	}
	if read.err != nil || read.out.Effect != runtime.NoEffect {
		t.Fatalf("reader failed: out=%+v err=%v", read.out, read.err)
	}
}
