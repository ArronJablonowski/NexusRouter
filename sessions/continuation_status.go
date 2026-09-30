package sessions

import "github.com/ArronJablonowski/NexusRouter/runtime"

// ContinuationStatus describes durable history eligibility, not provider,
// privacy, resource, context-budget or permission readiness for a new task.
// It never contains messages, recovered output, tool arguments or credentials.
type ContinuationStatus struct {
	Version         int    `json:"version"`
	TaskID          string `json:"task_id"`
	Sequence        int64  `json:"sequence"`
	State           string `json:"state"`
	HistoryEligible bool   `json:"history_eligible"`
	Reason          string `json:"reason"`
}

func (s ContinuationStatus) Validate() error {
	if s.Version != 1 || !ValidEventPageID(s.TaskID) || s.Sequence < 1 {
		return ErrHistory
	}
	switch s.State {
	case "running", "completed", "failed", "canceled":
	default:
		return ErrHistory
	}
	if s.HistoryEligible {
		if (s.Reason == "completed" && s.State == "completed") || ((s.Reason == "recovered_delegation" || s.Reason == "recovered_model") && s.State == "failed") {
			return nil
		}
		return ErrHistory
	}
	switch s.Reason {
	case "pending_tools", "uncertain_effects", "interrupted_turn", "history_ineligible":
		return nil
	case "task_running":
		if s.State == "running" {
			return nil
		}
	case "task_canceled":
		if s.State == "canceled" {
			return nil
		}
	case "task_failed":
		if s.State == "failed" {
			return nil
		}
	}
	return ErrHistory
}

// AssessContinuation assumes snapshot and tail come from validated replay of
// one coherent history. A failed task requires exactly its final two events.
// Callers must still perform all normal admission checks for a new request.
func AssessContinuation(snapshot Snapshot, tail []runtime.Event) ContinuationStatus {
	out := ContinuationStatus{Version: 1, TaskID: snapshot.TaskID, Sequence: snapshot.Sequence, State: snapshot.State, Reason: "history_ineligible"}
	if out.Validate() != nil {
		return out
	}
	switch {
	case len(snapshot.Pending) > 0:
		out.Reason = "pending_tools"
	case snapshot.UncertainEffects:
		out.Reason = "uncertain_effects"
	case snapshot.InterruptedTurn:
		out.Reason = "interrupted_turn"
	default:
		switch snapshot.State {
		case "completed":
			out.HistoryEligible, out.Reason = true, "completed"
		case "failed":
			out.Reason = "task_failed"
			if recoveredContinuationTail(snapshot, tail) {
				out.HistoryEligible, out.Reason = true, "recovered_delegation"
			}
		case "running":
			out.Reason = "task_running"
		case "canceled":
			out.Reason = "task_canceled"
		}
	}
	return out
}

func recoveredContinuationTail(snapshot Snapshot, tail []runtime.Event) bool {
	if snapshot.Sequence < 2 || len(tail) != 2 {
		return false
	}
	tool, end := tail[0], tail[1]
	return tool.TaskID == snapshot.TaskID && end.TaskID == snapshot.TaskID && tool.SessionID == snapshot.SessionID && end.SessionID == snapshot.SessionID && tool.Sequence == snapshot.Sequence-1 && end.Sequence == snapshot.Sequence && tool.Kind == runtime.ToolCompleted && tool.Data.Code == "delegation_recovered" && tool.Data.Effect == runtime.NoEffect && (tool.Data.ToolName == "delegate" || tool.Data.ToolName == "delegate_batch") && end.Kind == runtime.TaskFailed && end.Data.Code == "interrupted_after_delegation" && end.CausationID == tool.ID && end.TurnID == tool.TurnID && end.AttemptID == tool.AttemptID
}
