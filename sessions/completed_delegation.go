package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const (
	CompletedDelegationEvidenceVersion = 1
	maxCompletedDelegationInputBytes   = 8 << 20
	completedDelegationSubmissionID    = "delegation-projection"
)

// CompletedDelegationEvidence is an owned, sealed projection of one accepted
// worker lifecycle and its successfully validated execution. It deliberately
// retains only the boundary events and digests needed to bind later durable
// compaction evidence; the complete histories remain the caller's concern.
type CompletedDelegationEvidence struct {
	Version                 int                                   `json:"version"`
	ParentTaskID            string                                `json:"parent_task_id"`
	WorkTaskID              string                                `json:"work_task_id"`
	ExecutionTaskID         string                                `json:"execution_task_id"`
	SessionID               string                                `json:"session_id"`
	WorkerID                string                                `json:"worker_id"`
	Scope                   string                                `json:"scope"`
	Origin                  runtime.DelegationOrigin              `json:"origin"`
	Authority               runtime.DelegationCompactionAuthority `json:"authority"`
	WorkStart               runtime.Event                         `json:"work_start"`
	WorkStartDigest         string                                `json:"work_start_digest"`
	WorkTerminal            runtime.Event                         `json:"work_terminal"`
	WorkTerminalDigest      string                                `json:"work_terminal_digest"`
	ExecutionStart          runtime.Event                         `json:"execution_start"`
	ExecutionStartDigest    string                                `json:"execution_start_digest"`
	ExecutionContextDigest  string                                `json:"execution_context_digest"`
	ExecutionTerminal       runtime.Event                         `json:"execution_terminal"`
	ExecutionTerminalDigest string                                `json:"execution_terminal_digest"`
	ResultDigest            string                                `json:"result_digest"`
	EvidenceDigest          string                                `json:"evidence_digest"`
}

// ProjectCompletedDelegation accepts only a fully validated, accepted worker
// paired with its exact successful child execution. Empty submission IDs are
// supported for internal delegation histories by adding a sentinel solely to
// private validation clones; returned events always preserve the originals.
func ProjectCompletedDelegation(work, execution []runtime.Event) (CompletedDelegationEvidence, error) {
	bad := func() (CompletedDelegationEvidence, error) { return CompletedDelegationEvidence{}, ErrHistory }
	workCopy, executionCopy, err := cloneCompletedDelegationHistories(work, execution)
	if err != nil || len(workCopy) == 0 || len(executionCopy) == 0 {
		return bad()
	}
	originalWorkStart, originalWorkEnd := workCopy[0], workCopy[len(workCopy)-1]
	originalExecutionStart, originalExecutionEnd := executionCopy[0], executionCopy[len(executionCopy)-1]
	if completedDelegationHistoryHasRecoveryOrUncertainty(workCopy) || completedDelegationHistoryHasRecoveryOrUncertainty(executionCopy) {
		return bad()
	}
	for _, event := range workCopy {
		if event.SessionID != originalWorkStart.SessionID {
			return bad()
		}
	}
	for _, event := range executionCopy {
		if event.SessionID != originalExecutionStart.SessionID {
			return bad()
		}
	}

	workSubmission, executionSubmission := originalWorkStart.Data.SubmissionID, originalExecutionStart.Data.SubmissionID
	if workSubmission != executionSubmission {
		return bad()
	}
	if workSubmission == "" {
		workCopy[0].Data.SubmissionID = completedDelegationSubmissionID
		executionCopy[0].Data.SubmissionID = completedDelegationSubmissionID
	}

	text, _, accepted, err := projectWorkerTerminalAudit(workCopy)
	if err != nil || !accepted {
		return bad()
	}
	child, err := ProjectTerminalSubmission(executionCopy)
	if err != nil || child.State != "succeeded" || child.Result == nil || child.Result.Text != text {
		return bad()
	}
	result, err := recoveryWorkResult(workCopy, executionCopy)
	if err != nil {
		return bad()
	}

	start := originalWorkStart
	childStart := originalExecutionStart
	if start.Kind != runtime.TaskStarted || childStart.Kind != runtime.TaskStarted ||
		originalWorkEnd.Kind != runtime.TaskCompleted || originalExecutionEnd.Kind != runtime.TaskCompleted ||
		start.Data.DelegationCompaction == nil || start.Data.DelegationCompaction.Validate() != nil ||
		start.Data.DelegationOrigin == nil || start.Data.DelegationOrigin.Validate() != nil ||
		start.Data.ParentTaskID != start.Data.DelegationCompaction.RootTaskID ||
		childStart.Data.ParentTaskID != start.TaskID || childStart.Data.DelegationOrigin != nil ||
		start.TaskID == childStart.TaskID || start.TaskID == start.Data.ParentTaskID || childStart.TaskID == start.Data.ParentTaskID ||
		start.SessionID == "" || childStart.SessionID == "" || start.WorkerID == "" || len(childStart.Data.Messages) == 0 {
		return bad()
	}

	workStartDigest, err := completedDelegationEventDigest(originalWorkStart)
	if err != nil {
		return bad()
	}
	workEndDigest, err := completedDelegationEventDigest(originalWorkEnd)
	if err != nil {
		return bad()
	}
	childStartDigest, err := completedDelegationEventDigest(originalExecutionStart)
	if err != nil {
		return bad()
	}
	childEndDigest, err := completedDelegationEventDigest(originalExecutionEnd)
	if err != nil {
		return bad()
	}
	contextDigest, err := completedDelegationJSONDigest(childStart.Data.Messages)
	if err != nil {
		return bad()
	}
	resultDigest := sha256.Sum256(result)

	evidence := CompletedDelegationEvidence{
		Version:      CompletedDelegationEvidenceVersion,
		ParentTaskID: start.Data.ParentTaskID, WorkTaskID: start.TaskID, ExecutionTaskID: childStart.TaskID,
		SessionID: start.SessionID, WorkerID: start.WorkerID, Scope: start.Data.DelegationCompaction.Scope,
		Origin: *start.Data.DelegationOrigin.Clone(), Authority: *start.Data.DelegationCompaction.Clone(),
		WorkStart: originalWorkStart, WorkStartDigest: workStartDigest,
		WorkTerminal: originalWorkEnd, WorkTerminalDigest: workEndDigest,
		ExecutionStart: originalExecutionStart, ExecutionStartDigest: childStartDigest,
		ExecutionContextDigest: contextDigest,
		ExecutionTerminal:      originalExecutionEnd, ExecutionTerminalDigest: childEndDigest,
		ResultDigest: hex.EncodeToString(resultDigest[:]),
	}
	evidence.EvidenceDigest, err = evidence.canonicalDigest()
	if err != nil || evidence.Validate() != nil {
		return bad()
	}
	return evidence, nil
}

func completedDelegationHistoryHasRecoveryOrUncertainty(events []runtime.Event) bool {
	for _, event := range events {
		if event.Data.Effect == runtime.UncertainEffect || interruptedModelCode(event.Data.Code) || strings.Contains(event.Data.Code, "recover") {
			return true
		}
	}
	return false
}

// Validate detects mutation of the owned projection and all boundary evidence.
func (e CompletedDelegationEvidence) Validate() error {
	if e.Version != CompletedDelegationEvidenceVersion || !validLifecycleID(e.ParentTaskID) ||
		!validLifecycleID(e.WorkTaskID) || !validLifecycleID(e.ExecutionTaskID) || !validLifecycleID(e.SessionID) ||
		!validLifecycleID(e.WorkerID) || e.WorkTaskID == e.ExecutionTaskID || e.ParentTaskID == e.WorkTaskID || e.ParentTaskID == e.ExecutionTaskID ||
		e.Scope != "delegation-"+e.ParentTaskID || e.Origin.Validate() != nil || e.Authority.Validate() != nil ||
		e.Authority.RootTaskID != e.ParentTaskID || e.Authority.Scope != e.Scope ||
		!validLifecycleDigest(e.WorkStartDigest) || !validLifecycleDigest(e.WorkTerminalDigest) ||
		!validLifecycleDigest(e.ExecutionStartDigest) || !validLifecycleDigest(e.ExecutionTerminalDigest) ||
		!validLifecycleDigest(e.ExecutionContextDigest) || !validLifecycleDigest(e.ResultDigest) || !validLifecycleDigest(e.EvidenceDigest) {
		return ErrHistory
	}
	if e.WorkStart.Kind != runtime.TaskStarted || e.WorkTerminal.Kind != runtime.TaskCompleted ||
		e.ExecutionStart.Kind != runtime.TaskStarted || e.ExecutionTerminal.Kind != runtime.TaskCompleted ||
		e.WorkStart.TaskID != e.WorkTaskID || e.WorkTerminal.TaskID != e.WorkTaskID ||
		e.ExecutionStart.TaskID != e.ExecutionTaskID || e.ExecutionTerminal.TaskID != e.ExecutionTaskID ||
		e.WorkStart.SessionID != e.SessionID || e.WorkTerminal.SessionID != e.SessionID ||
		e.ExecutionStart.SessionID == "" || e.ExecutionTerminal.SessionID != e.ExecutionStart.SessionID ||
		e.WorkStart.WorkerID != e.WorkerID || e.WorkTerminal.WorkerID != e.WorkerID ||
		e.WorkStart.Data.ParentTaskID != e.ParentTaskID || e.ExecutionStart.Data.ParentTaskID != e.WorkTaskID ||
		e.WorkStart.Data.DelegationOrigin == nil || !reflect.DeepEqual(*e.WorkStart.Data.DelegationOrigin, e.Origin) ||
		e.WorkStart.Data.DelegationCompaction == nil || e.WorkStart.Data.DelegationCompaction.AuthorityDigest != e.Authority.AuthorityDigest ||
		len(e.ExecutionStart.Data.Messages) == 0 {
		return ErrHistory
	}
	checks := []struct {
		event  runtime.Event
		digest string
	}{
		{e.WorkStart, e.WorkStartDigest}, {e.WorkTerminal, e.WorkTerminalDigest},
		{e.ExecutionStart, e.ExecutionStartDigest}, {e.ExecutionTerminal, e.ExecutionTerminalDigest},
	}
	for _, check := range checks {
		digest, err := completedDelegationEventDigest(check.event)
		if err != nil || digest != check.digest {
			return ErrHistory
		}
	}
	contextDigest, err := completedDelegationJSONDigest(e.ExecutionStart.Data.Messages)
	if err != nil || contextDigest != e.ExecutionContextDigest {
		return ErrHistory
	}
	digest, err := e.canonicalDigest()
	if err != nil || digest != e.EvidenceDigest {
		return ErrHistory
	}
	return nil
}

func (e CompletedDelegationEvidence) canonicalDigest() (string, error) {
	e.EvidenceDigest = ""
	return completedDelegationJSONDigest(e)
}

func completedDelegationEventDigest(event runtime.Event) (string, error) {
	body, err := event.Encode()
	if err != nil || len(body) > maxCompletedDelegationInputBytes {
		return "", ErrHistory
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func completedDelegationJSONDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > maxCompletedDelegationInputBytes {
		return "", ErrHistory
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func cloneCompletedDelegationHistories(work, execution []runtime.Event) ([]runtime.Event, []runtime.Event, error) {
	if len(work) == 0 || len(execution) == 0 || len(work) > MaxTaskEvents || len(execution) > MaxTaskEvents {
		return nil, nil, ErrHistory
	}
	total := 0
	clone := func(in []runtime.Event) ([]runtime.Event, error) {
		out := make([]runtime.Event, len(in))
		for i := range in {
			body, err := in[i].Encode()
			if err != nil || len(body) > maxCompletedDelegationInputBytes-total {
				return nil, ErrHistory
			}
			total += len(body)
			if json.Unmarshal(body, &out[i]) != nil {
				return nil, ErrHistory
			}
		}
		return out, nil
	}
	w, err := clone(work)
	if err != nil {
		return nil, nil, err
	}
	x, err := clone(execution)
	if err != nil {
		return nil, nil, err
	}
	return w, x, nil
}
