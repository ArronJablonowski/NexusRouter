package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// PlanInterruptedWorker plans failure, never acceptance, of an unfinished
// supervisor journal with one resolved or provably interrupted-model child. The caller must
// independently prove and retain original process ownership loss transactionally.
func PlanInterruptedWorker(histories [][]runtime.Event, now time.Time) (InterruptionRecovery, error) {
	bad := func() (InterruptionRecovery, error) { return InterruptionRecovery{}, ErrHistory }
	now = now.UTC()
	if len(histories) != 2 || now.Year() < 1970 || now.Year() >= 2261 {
		return bad()
	}
	var work, child []runtime.Event
	budget, count := 8<<20, 0
	for _, history := range histories {
		if len(history) < 2 {
			return bad()
		}
		start := history[0]
		for _, e := range history {
			body, err := e.Encode()
			if err != nil || len(body) > budget || count >= 9999 || e.Time.After(now) || e.Time.UTC().Year() < 1970 || e.Time.UTC().Year() >= 2261 || !ValidEventPageID(e.ID) || e.CorrelationID != start.TaskID {
				return bad()
			}
			budget -= len(body)
			count++
			if e.Data.Effect != "" && e.Data.Effect != runtime.NoEffect {
				return bad()
			}
		}
		if start.WorkerID != "" {
			if work != nil {
				return bad()
			}
			work = history
		} else {
			if child != nil {
				return bad()
			}
			child = history
		}
	}
	if work == nil || child == nil {
		return bad()
	}
	start, execution := work[0], child[0]
	if !ValidEventPageID(start.TaskID) || !ValidEventPageID(start.SessionID) || !ValidEventPageID(start.WorkerID) || !ValidEventPageID(start.Data.ParentTaskID) || (start.Data.SubmissionID != "" && !ValidEventPageID(start.Data.SubmissionID)) || start.Data.ParentTaskID == start.TaskID || start.Data.RetryOfTaskID != "" || start.Data.DelegationOrigin == nil || start.Data.DelegationOrigin.Validate() != nil {
		return bad()
	}
	if !ValidEventPageID(execution.TaskID) || !ValidEventPageID(execution.SessionID) || execution.TaskID == start.TaskID || execution.TaskID == start.Data.ParentTaskID || execution.Data.SubmissionID != start.Data.SubmissionID || execution.Data.ParentTaskID != start.TaskID || execution.Data.RetryOfTaskID != "" || execution.Data.DelegationOrigin != nil {
		return bad()
	}
	for _, e := range child {
		if e.WorkerID != "" {
			return bad()
		}
	}
	childState, err := Replay(context.Background(), terminalReader(child), execution.TaskID)
	interrupted := child[len(child)-1].Data.Code == "interrupted_model"
	if err != nil || (childState.InterruptedTurn && !interrupted) || (interrupted && !isInterruptedModelTerminal(child)) || childState.UncertainEffects || len(childState.Pending) != 0 {
		return bad()
	}
	childValidation := append([]runtime.Event(nil), child...)
	if childValidation[0].Data.SubmissionID == "" {
		childValidation[0].Data.SubmissionID = "unsubmitted"
	}
	if _, err = ProjectTerminalSubmission(childValidation); err != nil {
		return bad()
	}
	// Appending a failure to the prefix reuses the strict supervisor lifecycle
	// validator: start, started, heartbeats, optional accepted evaluation/output.
	last := work[len(work)-1]
	seq := last.Sequence + 1
	sum := sha256.Sum256([]byte(start.TaskID + "\x00" + last.ID + "\x00" + strconv.FormatInt(seq, 10) + "\x00worker_owner_interrupted\x00" + now.Format(time.RFC3339Nano)))
	end := runtime.Event{Version: 1, ID: hex.EncodeToString(sum[:]), TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, WorkerID: start.WorkerID, CausationID: last.ID, Sequence: seq, Time: now, Kind: runtime.TaskFailed, Data: runtime.Data{Code: "worker_owner_interrupted"}}
	body, err := end.Encode()
	if err != nil || len(body) > budget {
		return bad()
	}
	for i, e := range work {
		if interrupted && (e.Kind == runtime.EvaluationRecorded || e.Kind == runtime.WorkerCompleted) {
			return bad()
		}
		if e.Data.Effect != "" || len(e.Data.Messages) != 0 || e.Data.Compaction != nil || (i > 0 && (e.Data.ParentTaskID != "" || e.Data.SubmissionID != "" || e.Data.DelegationOrigin != nil || e.Data.RetryOfTaskID != "")) {
			return bad()
		}
		allowed := runtime.Data{}
		switch e.Kind {
		case runtime.TaskStarted:
			allowed = runtime.Data{ParentTaskID: start.Data.ParentTaskID, SubmissionID: start.Data.SubmissionID, DelegationOrigin: start.Data.DelegationOrigin}
		case runtime.EvaluationRecorded:
			allowed = runtime.Data{Accepted: e.Data.Accepted, Code: "worker_validator"}
		case runtime.WorkerCompleted:
			allowed = runtime.Data{Text: e.Data.Text}
		}
		if !reflect.DeepEqual(e.Data, allowed) {
			return bad()
		}
	}
	if work[1].Kind != runtime.WorkerStarted {
		return bad()
	}
	full := append(append([]runtime.Event(nil), work...), end)
	// The legacy terminal-tree projector requires submitted work. Normalize only
	// its private validation copy for SDK work; the returned/source events retain
	// their original empty submission identity.
	validation := append([]runtime.Event(nil), full...)
	if validation[0].Data.SubmissionID == "" {
		validation[0].Data.SubmissionID = "unsubmitted"
	}
	if _, accepted, err := projectWorkerTerminal(validation); err != nil || accepted {
		return bad()
	}
	state, err := Replay(context.Background(), terminalReader(work), start.TaskID)
	if err != nil || state.State != "running" || state.InterruptedTurn || state.UncertainEffects || len(state.Pending) != 0 {
		return bad()
	}
	return InterruptionRecovery{ParentTaskID: start.TaskID, ExpectedSequence: state.Sequence, Events: []runtime.Event{end}}, nil
}

// InterruptedWorkerTreeRecovery is an atomic failure plan. Child, when present,
// must commit with Worker; neither plan authorizes dispatch or output acceptance.
type InterruptedWorkerTreeRecovery struct {
	Worker InterruptionRecovery
	Child  *InterruptionRecovery
}

// PlanInterruptedWorkerTree also handles a running, model-only execution child.
// Ownership loss must be independently proven for the entire tree by the caller.
func PlanInterruptedWorkerTree(histories [][]runtime.Event, now time.Time) (InterruptedWorkerTreeRecovery, error) {
	bad := func() (InterruptedWorkerTreeRecovery, error) { return InterruptedWorkerTreeRecovery{}, ErrHistory }
	now = now.UTC()
	if len(histories) != 2 {
		return bad()
	}
	childIndex := -1
	for i, h := range histories {
		if len(h) == 0 {
			return bad()
		}
		if h[0].WorkerID == "" {
			if childIndex != -1 {
				return bad()
			}
			childIndex = i
		}
	}
	if childIndex == -1 {
		return bad()
	}
	child := histories[childIndex]
	var childPlan *InterruptionRecovery
	validation := histories
	last := child[len(child)-1].Kind
	if last != runtime.TaskCompleted && last != runtime.TaskFailed && last != runtime.TaskCanceled {
		planned, err := PlanInterruptedModel([][]runtime.Event{child}, now, false)
		if err != nil {
			return bad()
		}
		childPlan = &planned
		validation = append([][]runtime.Event(nil), histories...)
		validation[childIndex] = append(append([]runtime.Event(nil), child...), planned.Events...)
	}
	worker, err := PlanInterruptedWorker(validation, now)
	if err != nil {
		return bad()
	}
	return InterruptedWorkerTreeRecovery{Worker: worker, Child: childPlan}, nil
}

// Recognize only the exact deterministic terminal derived from a model-only
// prefix. A matching failure code alone is not proof of safe interruption.
func isInterruptedModelTerminal(history []runtime.Event) bool {
	if len(history) < 2 {
		return false
	}
	end := history[len(history)-1]
	if end.Kind != runtime.TaskFailed || end.Data.Code != "interrupted_model" {
		return false
	}
	plan, err := PlanInterruptedModel([][]runtime.Event{history[:len(history)-1]}, end.Time, false)
	return err == nil && len(plan.Events) == 1 && reflect.DeepEqual(plan.Events[0], end)
}
