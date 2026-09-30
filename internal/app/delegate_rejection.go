package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type delegateFailureEvidence struct {
	TaskID   string       `json:"task_id"`
	Sequence int64        `json:"sequence"`
	Kind     runtime.Kind `json:"kind"`
	Code     string       `json:"code"`
}

type delegateFailure struct {
	Version     int                       `json:"version"`
	Error       string                    `json:"error"`
	Reason      string                    `json:"reason"`
	WorkID      string                    `json:"work_task_id"`
	ExecutionID string                    `json:"execution_task_id,omitempty"`
	Evidence    []delegateFailureEvidence `json:"evidence"`
}

// Project only replay-validated, terminal records owned by this delegation.
// Raw worker output, prompts, provider errors and credentials never enter this
// envelope. Evidence is diagnostic, not permission to retry uncertain effects.
// On missing/ambiguous storage evidence retain the original generic rejection.
func delegateRejection(ctx context.Context, db *telemetry.Store, parent, session, workID, executionID string) runtime.ToolResult {
	failed := runtime.ToolResult{Content: `{"error":"delegate_unavailable_or_rejected"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
	uncertain := failed
	uncertain.Effect, uncertain.Recoverable = runtime.UncertainEffect, false
	if db == nil || ctx == nil {
		if executionID != "" {
			return uncertain
		}
		return failed
	}
	query, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	// A joined worker is not evidence that its last tool completed without an
	// effect. Preserve an uncertain or missing execution journal as terminal;
	// never relabel it as a repairable rejection merely because tools were
	// advertised read-only. Tool panics and invalid outcomes remain uncertain.
	if executionID != "" {
		execution, err := db.TaskSnapshot(query, executionID)
		if err != nil || execution.ParentTaskID != workID || execution.UncertainEffects {
			return uncertain
		}
		for _, call := range execution.Pending {
			if call.Dispatched {
				return uncertain
			}
		}
	}
	work, err := db.TaskSnapshot(query, workID)
	if err != nil || work.ParentTaskID != parent || work.SessionID != session || (work.State != "failed" && work.State != "canceled") {
		return failed
	}
	project := func(id string, seq int64) (delegateFailureEvidence, bool) {
		page, err := db.ReadEventPage(query, id, seq-1, 1)
		if err != nil || page.HeadSequence != seq || page.HasMore || len(page.Events) != 1 {
			return delegateFailureEvidence{}, false
		}
		e := page.Events[0]
		if e.Kind != runtime.TaskFailed && e.Kind != runtime.TaskCanceled {
			return delegateFailureEvidence{}, false
		}
		code := e.Data.Code
		switch code {
		case "worker_failed", "canceled", "invalid_output", "empty_output", "execution_failed", "budget_exhausted", "provider_retryable_no_output", "provider_failed_before_tools", "execution_lease_lost":
		default:
			return delegateFailureEvidence{}, false
		}
		return delegateFailureEvidence{TaskID: id, Sequence: seq, Kind: e.Kind, Code: code}, true
	}
	w, ok := project(workID, work.Sequence)
	if !ok {
		return failed
	}
	report := delegateFailure{Version: 1, Error: "delegate_unavailable_or_rejected", Reason: w.Code, WorkID: workID, Evidence: []delegateFailureEvidence{w}}
	if work.State == "canceled" {
		report.Reason = "canceled"
	}
	if executionID != "" {
		execution, err := db.TaskSnapshot(query, executionID)
		if err != nil || execution.ParentTaskID != workID {
			return failed
		}
		switch execution.State {
		case "failed", "canceled":
			e, ok := project(executionID, execution.Sequence)
			if !ok {
				return failed
			}
			report.ExecutionID = executionID
			report.Evidence = append(report.Evidence, e)
			if work.State != "canceled" {
				report.Reason = e.Code
			}
		case "completed":
			// Execution completion does not imply supervisor acceptance.
			report.ExecutionID = executionID
		default:
			return failed
		}
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) > 2048 {
		return failed
	}
	failed.Content = string(body)
	return failed
}
