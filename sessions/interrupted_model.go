package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// PlanInterruptedModel records failure of an unfinished model-only journal,
// including pre-dispatch and post-turn boundaries lacking a terminal receipt.
// It neither reconstructs partial text nor authorizes a retry. Prior tool pairs
// embedded in TaskStarted.Messages are context, never current tool execution.
// Unsupported journals return ErrHistory with no partial plan.
func PlanInterruptedModel(histories [][]runtime.Event, now time.Time, canceled bool) (InterruptionRecovery, error) {
	return planInterruptedModel(histories, now, canceled, false)
}

// PlanInterruptedReadOnlyModel fails an interrupted execution child only after
// every current-journal tool has explicitly completed a read-only, no-effect
// execution. It never treats tool output as accepted work or authorizes retry.
func PlanInterruptedReadOnlyModel(histories [][]runtime.Event, now time.Time) (InterruptionRecovery, error) {
	return planInterruptedModel(histories, now.UTC(), false, true)
}

func planInterruptedModel(histories [][]runtime.Event, now time.Time, canceled, readOnly bool) (InterruptionRecovery, error) {
	bad := func() (InterruptionRecovery, error) { return InterruptionRecovery{}, ErrHistory }
	if len(histories) != 1 || len(histories[0]) < 1 || len(histories[0]) > 10000 || now.IsZero() || now.Year() < 1970 || now.Year() >= 2261 {
		return bad()
	}
	history := histories[0]
	start := history[0]
	if readOnly && start.Data.ParentTaskID == "" {
		return bad()
	}
	if !ValidEventPageID(start.TaskID) || !ValidEventPageID(start.SessionID) || start.Data.RetryOfTaskID != "" || (start.Data.ParentTaskID != "" && (!ValidEventPageID(start.Data.ParentTaskID) || start.Data.ParentTaskID == start.TaskID)) {
		return bad()
	}
	budget := 8 << 20
	var active runtime.Event
	toolExecutions := 0
	for _, event := range history {
		body, err := event.Encode()
		if err != nil || len(body) > budget || event.Time.After(now) || event.Time.Year() < 1970 || event.Time.Year() >= 2261 || !ValidEventPageID(event.ID) || event.CorrelationID != start.TaskID || event.WorkerID != "" || event.Data.DelegationOrigin != nil {
			return bad()
		}
		if !readOnly {
			if event.Data.ToolCallID != "" || event.Data.ToolName != "" || event.Data.Effect != "" || len(event.Data.ToolCalls) != 0 {
				return bad()
			}
		} else if event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted {
			if event.Data.ToolBehavior != runtime.BehaviorReadOnly || event.Data.ToolName == "delegate" || event.Data.ToolName == "delegate_batch" || len(event.Data.ToolCalls) != 0 || (event.Kind == runtime.ToolCompleted && event.Data.Effect != runtime.NoEffect) || (event.Kind == runtime.ToolStarted && event.Data.Effect != runtime.NoEffect && event.Data.Effect != runtime.UncertainEffect) {
				return bad()
			}
			if event.Kind == runtime.ToolCompleted {
				toolExecutions++
			}
		} else {
			if event.Data.ToolCallID != "" || event.Data.ToolName != "" || event.Data.Effect != "" || (len(event.Data.ToolCalls) != 0 && event.Kind != runtime.TurnCompleted) {
				return bad()
			}
			for _, call := range event.Data.ToolCalls {
				if call.Name == "delegate" || call.Name == "delegate_batch" {
					return bad()
				}
			}
		}
		budget -= len(body)
		contextCompaction := event.Kind == runtime.ContextCompacted
		if event.Data.Accepted != nil || (!contextCompaction && event.Kind != runtime.TaskStarted && (len(event.Data.Messages) != 0 || event.Data.ParentTaskID != "" || event.Data.RetryOfTaskID != "")) {
			return bad()
		}
		if (event.TurnID != "" && !ValidEventPageID(event.TurnID)) || (event.AttemptID != "" && !ValidEventPageID(event.AttemptID)) {
			return bad()
		}
		switch event.Kind {
		case runtime.TaskStarted, runtime.ModelDelta, runtime.RouteSelected, runtime.SteeringApplied:
		case runtime.ContextCompacted:
			active = event
		case runtime.TurnStarted:
			active = event
		case runtime.TurnCompleted:
		case runtime.ToolStarted, runtime.ToolCompleted:
			if !readOnly {
				return bad()
			}
		default:
			// Error/evaluation records have no model-only attribution contract
			// here; reject rather than assume they contain no approval evidence.
			return bad()
		}
	}
	if readOnly && toolExecutions == 0 {
		return bad()
	}
	snapshot, err := Replay(context.Background(), terminalReader(history), start.TaskID)
	if err != nil || snapshot.State != "running" || snapshot.UncertainEffects || len(snapshot.Pending) != 0 {
		return bad()
	}
	kind, code := runtime.TaskFailed, "interrupted_model"
	if readOnly {
		code = "interrupted_read_only_model"
	}
	if canceled {
		kind, code = runtime.TaskCanceled, "canceled"
	}
	sequence := snapshot.Sequence + 1
	sum := sha256.Sum256([]byte(start.TaskID + "\x00" + history[len(history)-1].ID + "\x00" + strconv.FormatInt(sequence, 10) + "\x00" + string(kind) + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	terminal := runtime.Event{Version: 1, ID: hex.EncodeToString(sum[:]), TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, Sequence: sequence, Time: now.UTC(), Kind: kind, TurnID: active.TurnID, AttemptID: active.AttemptID, CausationID: active.ID, Data: runtime.Data{Code: code}}
	if terminal.CausationID == "" {
		terminal.CausationID = history[len(history)-1].ID
	}
	if readOnly {
		body, err := terminal.Encode()
		if err != nil || len(history) >= 10000 || len(body) > budget {
			return bad()
		}
	}
	full := append(append([]runtime.Event(nil), history...), terminal)
	verified, err := Replay(context.Background(), terminalReader(full), start.TaskID)
	if err != nil || verified.InterruptedTurn != snapshot.InterruptedTurn || (verified.State != "failed" && verified.State != "canceled") {
		return bad()
	}
	return InterruptionRecovery{ParentTaskID: start.TaskID, ExpectedSequence: snapshot.Sequence, Events: []runtime.Event{terminal}}, nil
}
