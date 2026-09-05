package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type auditDelegation struct {
	parent     runtime.Event
	report     delegateFailure
	success    *auditDelegateSuccess
	batchIndex *int
}

// Retained for callers inspecting one single-delegation envelope.
func auditDelegationReference(e runtime.Event) (*auditDelegation, error) {
	if e.Data.ToolName != "delegate" {
		return nil, nil
	}
	refs, err := auditDelegationReferences(e)
	if err != nil || len(refs) == 0 {
		return nil, err
	}
	return &refs[0], nil
}

type auditChildHistory struct {
	snapshot sessions.Snapshot
	events   []runtime.Event
}

// Paged payload bounds apply to the entire child graph. TaskSnapshot separately
// proves replay with its storage bounds (8 MiB/10,000 events per task); paged
// reads then verify the same terminal head. No write,
// recovery, provider dispatch or context.WithoutCancel occurs here.
func readAuditChild(ctx context.Context, db *telemetry.Store, id string, budget *int) (auditChildHistory, error) {
	var out auditChildHistory
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	snapshot, err := db.TaskSnapshot(ctx, id)
	if err != nil || snapshot.Sequence > 1000 || snapshot.Sequence < 1 || snapshot.InterruptedTurn || snapshot.UncertainEffects || len(snapshot.Pending) > 0 {
		return out, ErrAdmission
	}
	var seq int64
	for pageNumber := 0; pageNumber < 32; pageNumber++ {
		page, err := db.ReadEventPage(ctx, id, seq, 100)
		if err != nil || page.HeadSequence != snapshot.Sequence || page.SessionID != snapshot.SessionID || page.State != snapshot.State {
			return out, ErrAdmission
		}
		for _, e := range page.Events {
			body, err := json.Marshal(e)
			if err != nil || len(body) > *budget || e.Sequence != seq+1 {
				return out, ErrAdmission
			}
			*budget -= len(body)
			seq = e.Sequence
			out.events = append(out.events, e)
		}
		if !page.HasMore {
			break
		}
		if pageNumber == 31 || len(page.Events) == 0 {
			return out, ErrAdmission
		}
	}
	if seq != snapshot.Sequence || ctx.Err() != nil {
		return out, ErrAdmission
	}
	out.snapshot = snapshot
	return out, nil
}

func auditDelegatedEvidence(ctx context.Context, db *telemetry.Store, delegations []auditDelegation, secrets []string) ([]evaluation.ReviewEvidence, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if len(delegations) > 8 {
		return nil, ErrAdmission
	}
	budget := 1 << 20
	var out []evaluation.ReviewEvidence
	seen := map[string]bool{}
	for _, d := range delegations {
		work, err := readAuditChild(ctx, db, d.report.WorkID, &budget)
		if err != nil || seen[d.report.WorkID] || work.snapshot.ParentTaskID != d.parent.TaskID || work.snapshot.SessionID != d.parent.SessionID {
			return nil, ErrAdmission
		}
		if !auditDelegationOriginMatches(d, work) {
			return nil, ErrAdmission
		}
		if (d.success == nil && work.snapshot.State != "failed" && work.snapshot.State != "canceled") || (d.success != nil && work.snapshot.State != "completed") {
			return nil, ErrAdmission
		}
		seen[d.report.WorkID] = true
		histories := []auditChildHistory{work}
		if d.report.ExecutionID != "" {
			child, err := readAuditChild(ctx, db, d.report.ExecutionID, &budget)
			if err != nil || seen[d.report.ExecutionID] || child.snapshot.ParentTaskID != d.report.WorkID || (child.snapshot.State != "failed" && child.snapshot.State != "canceled" && child.snapshot.State != "completed") {
				return nil, ErrAdmission
			}
			seen[d.report.ExecutionID] = true
			histories = append(histories, child)
		}
		if d.success != nil && (len(histories) != 2 || histories[1].snapshot.State != "completed" || !auditSuccessfulDelegation(work, histories[1], d.success.Output)) {
			return nil, ErrAdmission
		}
		var refs []delegateFailureEvidence
		reason := ""
		for index, h := range histories {
			terminal := h.events[len(h.events)-1]
			if terminal.Kind == runtime.TaskFailed || terminal.Kind == runtime.TaskCanceled {
				switch terminal.Data.Code {
				case "worker_failed", "canceled", "invalid_output", "empty_output", "execution_failed", "budget_exhausted", "provider_retryable_no_output", "execution_lease_lost":
				default:
					return nil, ErrAdmission
				}
				refs = append(refs, delegateFailureEvidence{TaskID: terminal.TaskID, Sequence: terminal.Sequence, Kind: terminal.Kind, Code: terminal.Data.Code})
				reason = terminal.Data.Code
			}
			role := "work"
			if index == 1 {
				role = "execution"
			}
			projected, err := projectAuditChildItem(d.parent.Sequence, d.batchIndex, role, h.events, secrets)
			if err != nil {
				return nil, err
			}
			out = append(out, projected...)
			if !boundAuditExecutionEvidence(out) {
				return nil, ErrAdmission
			}
		}
		if work.snapshot.State == "canceled" {
			reason = "canceled"
		}
		expected, _ := json.Marshal(refs)
		actual, _ := json.Marshal(d.report.Evidence)
		if d.success == nil && (!bytes.Equal(expected, actual) || d.report.Reason != reason) {
			return nil, ErrAdmission
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}

func projectAuditChild(parentSequence int64, role string, events []runtime.Event, secrets []string) ([]evaluation.ReviewEvidence, error) {
	return projectAuditChildItem(parentSequence, nil, role, events, secrets)
}

func projectAuditChildItem(parentSequence int64, batchIndex *int, role string, events []runtime.Event, secrets []string) ([]evaluation.ReviewEvidence, error) {
	var turn, attempt string
	var out []evaluation.ReviewEvidence
	for _, e := range events {
		if e.Kind == runtime.TurnStarted {
			turn, attempt = e.TurnID, e.AttemptID
		}
		if e.Kind != runtime.EvaluationRecorded && e.Kind != runtime.TaskFailed && e.Kind != runtime.TaskCanceled && e.Kind != runtime.TaskCompleted {
			continue
		}
		if e.Kind == runtime.EvaluationRecorded {
			if role == "work" {
				if e.TurnID != "" || e.AttemptID != "" || e.WorkerID == "" || e.Data.Code != "worker_validator" || e.Data.Accepted == nil {
					return nil, ErrAdmission
				}
			} else if turn == "" || attempt == "" || e.TurnID != turn || e.AttemptID != attempt || e.Data.Accepted == nil {
				return nil, ErrAdmission
			}
		}
		p := struct {
			Version            int          `json:"version"`
			ParentToolSequence int64        `json:"parent_tool_sequence"`
			BatchIndex         *int         `json:"batch_index,omitempty"`
			Role               string       `json:"role"`
			TaskID             string       `json:"task_id"`
			Sequence           int64        `json:"sequence"`
			Kind               runtime.Kind `json:"kind"`
			TurnID             string       `json:"turn_id,omitempty"`
			AttemptID          string       `json:"attempt_id,omitempty"`
			Code               string       `json:"code,omitempty"`
			Validation         string       `json:"validation,omitempty"`
			Accepted           *bool        `json:"accepted,omitempty"`
		}{Version: 1, ParentToolSequence: parentSequence, BatchIndex: batchIndex, Role: role, TaskID: e.TaskID, Sequence: e.Sequence, Kind: e.Kind, TurnID: e.TurnID, AttemptID: e.AttemptID, Code: e.Data.Code}
		if e.Kind == runtime.EvaluationRecorded {
			p.Validation, p.Accepted = e.Data.Validation, e.Data.Accepted
		}
		for _, field := range []*string{&p.TaskID, &p.TurnID, &p.AttemptID, &p.Code, &p.Validation} {
			if len(*field) > 4096 {
				return nil, ErrAdmission
			}
			*field = redact(*field, secrets)
		}
		body, err := json.Marshal(p)
		content := redact(string(body), secrets)
		if err != nil || len(content) > 64<<10 || !json.Valid([]byte(content)) {
			return nil, ErrAdmission
		}
		prefix := "delegated_" + strconv.FormatInt(parentSequence, 10)
		if batchIndex != nil {
			prefix += "_item_" + strconv.Itoa(*batchIndex)
		}
		out = append(out, evaluation.ReviewEvidence{ID: prefix + "_" + role + "_" + strconv.FormatInt(e.Sequence, 10), Content: content})
	}
	return out, nil
}

func boundAuditExecutionEvidence(items []evaluation.ReviewEvidence) bool {
	if len(items) > 252 {
		return false
	}
	remaining := 64 << 10
	for _, item := range items {
		if len(item.Content) > remaining {
			return false
		}
		remaining -= len(item.Content)
	}
	return true
}
