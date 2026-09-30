package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func completedDelegationFixture(t *testing.T, submitted bool) ([]runtime.Event, []runtime.Event) {
	t.Helper()
	work := workerTerminalFixture()
	work[4].Data.Text = "answer"
	execution := terminalHistory(t, "success")
	for i := range execution {
		execution[i].TaskID = "execution-task"
		execution[i].SessionID = "session"
		execution[i].CorrelationID = "execution-task"
		execution[i].ID = "execution-event-" + execution[i].ID
	}
	work[0].Data.ParentTaskID = "parent-task"
	work[0].Data.DelegationOrigin = &runtime.DelegationOrigin{
		Version: 1, TurnID: "parent-turn", AttemptID: "parent-attempt", ToolCallID: "delegate-call", ToolName: "delegate",
	}
	authority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID: "parent-task", PlanDigest: strings.Repeat("a", 64), InheritedEngineDigest: strings.Repeat("b", 64),
		Scope: "delegation-parent-task", ParentPolicy: json.RawMessage(`{"rules":["read"]}`), ChildPolicy: json.RawMessage(`{"rules":["read"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	work[0].Data.DelegationCompaction = &authority
	execution[0].Data.ParentTaskID = "worker-task"
	if submitted {
		work[0].Data.SubmissionID = "submission"
		execution[0].Data.SubmissionID = "submission"
	} else {
		work[0].Data.SubmissionID = ""
		execution[0].Data.SubmissionID = ""
	}
	return work, execution
}

func TestProjectCompletedDelegationSubmittedAndInternal(t *testing.T) {
	for _, submitted := range []bool{true, false} {
		t.Run(map[bool]string{true: "submitted", false: "internal"}[submitted], func(t *testing.T) {
			work, execution := completedDelegationFixture(t, submitted)
			vw, vx, cloneErr := cloneCompletedDelegationHistories(work, execution)
			if !submitted {
				vw[0].Data.SubmissionID, vx[0].Data.SubmissionID = completedDelegationSubmissionID, completedDelegationSubmissionID
			}
			_, _, workerOK, workerErr := projectWorkerTerminalAudit(vw)
			child, childErr := ProjectTerminalSubmission(vx)
			recoveryResult, recoveryErr := recoveryWorkResult(vw, vx)
			if cloneErr != nil || workerErr != nil || !workerOK || childErr != nil || child.State != "succeeded" || recoveryErr != nil {
				t.Fatalf("fixture invalid clone=%v worker=%v/%v child=%v/%v recovery=%v", cloneErr, workerOK, workerErr, child.State, childErr, recoveryErr)
			}
			beforeWork, _ := json.Marshal(work)
			beforeExecution, _ := json.Marshal(execution)
			evidence, err := ProjectCompletedDelegation(work, execution)
			if err != nil || evidence.Validate() != nil {
				t.Fatal(evidence, err)
			}
			if evidence.ParentTaskID != "parent-task" || evidence.WorkTaskID != "worker-task" || evidence.ExecutionTaskID != "execution-task" ||
				evidence.WorkerID != "worker" || evidence.SessionID != "session" || evidence.Scope != "delegation-parent-task" ||
				evidence.Origin.ToolCallID != "delegate-call" || evidence.Authority.Validate() != nil || evidence.ExecutionContextDigest == "" {
				t.Fatal("incomplete evidence", evidence)
			}
			resultSum := sha256.Sum256(recoveryResult)
			contextBody, _ := json.Marshal(execution[0].Data.Messages)
			contextSum := sha256.Sum256(contextBody)
			if evidence.ResultDigest != hex.EncodeToString(resultSum[:]) || evidence.ExecutionContextDigest != hex.EncodeToString(contextSum[:]) {
				t.Fatal("result or execution context digest was not exact")
			}
			afterWork, _ := json.Marshal(work)
			afterExecution, _ := json.Marshal(execution)
			if !bytes.Equal(beforeWork, afterWork) || !bytes.Equal(beforeExecution, afterExecution) {
				t.Fatal("projector mutated input")
			}
			if evidence.WorkStart.Data.SubmissionID != work[0].Data.SubmissionID || evidence.ExecutionStart.Data.SubmissionID != execution[0].Data.SubmissionID {
				t.Fatal("private submission normalization escaped")
			}
			for _, pair := range []struct {
				event  runtime.Event
				digest string
			}{
				{work[0], evidence.WorkStartDigest}, {work[len(work)-1], evidence.WorkTerminalDigest},
				{execution[0], evidence.ExecutionStartDigest}, {execution[len(execution)-1], evidence.ExecutionTerminalDigest},
			} {
				body, encodeErr := pair.event.Encode()
				sum := sha256.Sum256(body)
				if encodeErr != nil || pair.digest != hex.EncodeToString(sum[:]) {
					t.Fatal("noncanonical event digest")
				}
			}
			work[0].Data.DelegationCompaction.ParentPolicy[0] = 'x'
			execution[0].Data.Messages[0].Content = "mutated"
			if evidence.Validate() != nil || evidence.Authority.ParentPolicy[0] == 'x' || evidence.ExecutionStart.Data.Messages[0].Content == "mutated" {
				t.Fatal("projection borrowed caller storage")
			}
		})
	}
}

func TestProjectCompletedDelegationAllowsIsolatedChildSession(t *testing.T) {
	work, execution := completedDelegationFixture(t, false)
	for i := range execution {
		execution[i].SessionID = "isolated-child-session"
	}
	evidence, err := ProjectCompletedDelegation(work, execution)
	if err != nil || evidence.Validate() != nil || evidence.SessionID != work[0].SessionID ||
		evidence.ExecutionStart.SessionID != "isolated-child-session" {
		t.Fatal("isolated child session was not preserved", evidence, err)
	}
}

func TestProjectCompletedDelegationRejectsUnprovenPairs(t *testing.T) {
	for name, mutate := range map[string]func([]runtime.Event, []runtime.Event){
		"submission mismatch":  func(w, x []runtime.Event) { x[0].Data.SubmissionID = "other" },
		"one empty submission": func(w, x []runtime.Event) { w[0].Data.SubmissionID = "" },
		"rejected worker":      func(w, x []runtime.Event) { no := false; w[3].Data.Accepted = &no },
		"failed worker":        func(w, x []runtime.Event) { w[5].Kind = runtime.TaskFailed; w[5].Data.Code = "worker_failed" },
		"canceled worker":      func(w, x []runtime.Event) { w[5].Kind = runtime.TaskCanceled; w[5].Data.Code = "canceled" },
		"failed child": func(w, x []runtime.Event) {
			x[len(x)-1].Kind = runtime.TaskFailed
			x[len(x)-1].Data.Code = "execution_failed"
		},
		"canceled child": func(w, x []runtime.Event) {
			x[len(x)-1].Kind = runtime.TaskCanceled
			x[len(x)-1].Data.Code = "canceled"
		},
		"output mismatch":   func(w, x []runtime.Event) { w[4].Data.Text = "invented" },
		"wrong child link":  func(w, x []runtime.Event) { x[0].Data.ParentTaskID = "other-work" },
		"wrong session":     func(w, x []runtime.Event) { x[0].SessionID = "other" },
		"bad origin":        func(w, x []runtime.Event) { w[0].Data.DelegationOrigin.ToolName = "shell" },
		"missing authority": func(w, x []runtime.Event) { w[0].Data.DelegationCompaction = nil },
		"bad authority":     func(w, x []runtime.Event) { w[0].Data.DelegationCompaction.PlanDigest = strings.Repeat("c", 64) },
		"child origin":      func(w, x []runtime.Event) { x[0].Data.DelegationOrigin = w[0].Data.DelegationOrigin.Clone() },
		"uncertain child":   func(w, x []runtime.Event) { x[len(x)-1].Data.Effect = runtime.UncertainEffect },
		"interrupted child": func(w, x []runtime.Event) { x[len(x)-1].Data.Code = "interrupted_model" },
		"recovery child":    func(w, x []runtime.Event) { x[len(x)-1].Data.Code = "delegation_recovered" },
		"missing context":   func(w, x []runtime.Event) { x[0].Data.Messages = nil },
		"oversize": func(w, x []runtime.Event) {
			x[0].Data.Messages[0].Content = strings.Repeat("x", maxCompletedDelegationInputBytes)
		},
	} {
		t.Run(name, func(t *testing.T) {
			work, execution := completedDelegationFixture(t, true)
			mutate(work, execution)
			if evidence, err := ProjectCompletedDelegation(work, execution); err == nil || evidence.Version != 0 {
				t.Fatal("unproven delegation accepted", evidence, err)
			}
		})
	}
}

func TestCompletedDelegationEvidenceDetectsMutation(t *testing.T) {
	work, execution := completedDelegationFixture(t, true)
	base, err := ProjectCompletedDelegation(work, execution)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*CompletedDelegationEvidence){
		"identity":           func(e *CompletedDelegationEvidence) { e.WorkTaskID = "other" },
		"origin":             func(e *CompletedDelegationEvidence) { e.Origin.ToolCallID = "other" },
		"authority":          func(e *CompletedDelegationEvidence) { e.Authority.PlanDigest = strings.Repeat("c", 64) },
		"work event":         func(e *CompletedDelegationEvidence) { e.WorkStart.ID = "other" },
		"execution messages": func(e *CompletedDelegationEvidence) { e.ExecutionStart.Data.Messages[0].Content = "other" },
		"boundary digest":    func(e *CompletedDelegationEvidence) { e.ExecutionTerminalDigest = strings.Repeat("0", 64) },
		"context digest":     func(e *CompletedDelegationEvidence) { e.ExecutionContextDigest = strings.Repeat("0", 64) },
		"result digest":      func(e *CompletedDelegationEvidence) { e.ResultDigest = strings.Repeat("0", 64) },
		"seal":               func(e *CompletedDelegationEvidence) { e.EvidenceDigest = strings.Repeat("0", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(base)
			var candidate CompletedDelegationEvidence
			if json.Unmarshal(body, &candidate) != nil {
				t.Fatal("clone")
			}
			mutate(&candidate)
			if candidate.Validate() == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
}
