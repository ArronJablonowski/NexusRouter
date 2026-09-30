package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// PlanInterruptedReadOnlyTools resolves dispatched read-only calls as failures,
// never as observed output or successful execution. The caller must prove the
// original execution image stopped and retain all child-holder checks through
// the atomic recovery commit. This planner never grants retry authority.
func PlanInterruptedReadOnlyTools(histories [][]runtime.Event, now time.Time) (InterruptionRecovery, error) {
	bad := func() (InterruptionRecovery, error) { return InterruptionRecovery{}, ErrHistory }
	now = now.UTC()
	if len(histories) != 1 || len(histories[0]) < 1 || len(histories[0]) > 9998 || now.Year() < 1970 || now.Year() >= 2261 {
		return bad()
	}
	history := histories[0]
	budget := 8 << 20
	for _, e := range history {
		body, err := e.Encode()
		if err != nil || len(body) > budget {
			return bad()
		}
		budget -= len(body)
	}
	start := history[0]
	snapshot, err := Replay(context.Background(), terminalReader(history), start.TaskID)
	// Replay conservatively marks dispatched pending calls uncertain. Only the
	// explicit read-only declarations below permit this narrow failure plan;
	// completed effect evidence is separately rejected by shared validation.
	if err != nil || snapshot.State != "running" || snapshot.InterruptedTurn || len(snapshot.Pending) < 1 || len(snapshot.Pending) > 32 {
		return bad()
	}
	for _, pending := range snapshot.Pending {
		if !pending.Dispatched || pending.ToolBehavior != runtime.BehaviorReadOnly || pending.Call.Name == "delegate" || pending.Call.Name == "delegate_batch" {
			return bad()
		}
	}
	if len(history)+len(snapshot.Pending)+1 > 10000 {
		return bad()
	}
	last := history[len(history)-1]
	previous := last.ID
	suffix := make([]runtime.Event, 0, len(snapshot.Pending)+1)
	for _, e := range history {
		if e.Kind != runtime.ToolStarted {
			continue
		}
		pending, exists := snapshot.Pending[e.Data.ToolCallID]
		if !exists {
			continue
		}
		sequence := snapshot.Sequence + int64(len(suffix)) + 1
		id := interruptedReadOnlyToolID(start.TaskID, previous, sequence, "tool_failed", pending.Call.ID, now)
		completed := runtime.Event{Version: 1, ID: id, TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, Sequence: sequence, Time: now, Kind: runtime.ToolCompleted, TurnID: pending.TurnID, AttemptID: pending.AttemptID, CausationID: e.ID, Data: runtime.Data{ToolCallID: pending.Call.ID, ToolName: pending.Call.Name, ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.NoEffect, Code: "tool_failed", Text: `{"error":"read_only_tool_interrupted"}`}}
		suffix = append(suffix, completed)
		previous = id
	}
	if len(suffix) != len(snapshot.Pending) {
		return bad()
	}
	resolved := append(append([]runtime.Event(nil), history...), suffix...)
	// Reuse the established strict execution-child/read-only history validator;
	// its synthetic terminal is not published by this distinct recovery protocol.
	verified, err := PlanInterruptedReadOnlyModel([][]runtime.Event{resolved}, now)
	if err != nil {
		return bad()
	}
	terminal := verified.Events[0]
	terminal.Data.Code = "interrupted_read_only_tool"
	terminal.CausationID = last.ID
	terminal.ID = interruptedReadOnlyToolID(start.TaskID, previous, terminal.Sequence, terminal.Data.Code, last.ID, now)
	suffix = append(suffix, terminal)
	for _, e := range suffix {
		body, err := e.Encode()
		if err != nil || len(body) > budget {
			return bad()
		}
		budget -= len(body)
	}
	full := append(append([]runtime.Event(nil), history...), suffix...)
	final, err := Replay(context.Background(), terminalReader(full), start.TaskID)
	if err != nil || final.State != "failed" || final.InterruptedTurn || final.UncertainEffects || len(final.Pending) != 0 {
		return bad()
	}
	return InterruptionRecovery{ParentTaskID: start.TaskID, ExpectedSequence: snapshot.Sequence, Events: suffix}, nil
}

func interruptedReadOnlyToolID(task, previous string, sequence int64, reason, call string, now time.Time) string {
	sum := sha256.Sum256([]byte(task + "\x00" + previous + "\x00" + strconv.FormatInt(sequence, 10) + "\x00" + reason + "\x00" + call + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

// A terminal code alone is not evidence: bind its original prefix and compare
// every synthesized tool failure and terminal against the deterministic plan.
func isInterruptedReadOnlyToolsTerminal(history []runtime.Event) bool {
	if len(history) < 3 {
		return false
	}
	end := history[len(history)-1]
	if end.Kind != runtime.TaskFailed || end.Data.Code != "interrupted_read_only_tool" {
		return false
	}
	prefixEnd := -1
	for i, e := range history[:len(history)-1] {
		if e.ID == end.CausationID {
			if prefixEnd != -1 {
				return false
			}
			prefixEnd = i
		}
	}
	if prefixEnd < 0 || len(history)-prefixEnd < 3 {
		return false
	}
	plan, err := PlanInterruptedReadOnlyTools([][]runtime.Event{history[:prefixEnd+1]}, end.Time)
	return err == nil && reflect.DeepEqual(plan.Events, history[prefixEnd+1:])
}
