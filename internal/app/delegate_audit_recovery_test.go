package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestPendingDelegationAuditInterruptedWorkerRecoversOnlyFailure(t *testing.T) {
	ctx := context.Background()
	svc, _ := autoFixture(t)
	db, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queued, err := svc.Submit(ctx, "pending-delegation-audit-recovery", Request{ModelID: "a", Prompt: "recovery fixture"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != queued.ID {
		t.Fatal("claim recovery fixture", claim.Status, err)
	}
	const parent, work, child, session = "audit-parent", "audit-work", "audit-child", "audit-session"
	submission := queued.ID
	key := delegationAuditKey(work)
	operation := auditOperationID(key)
	intent := &runtime.DelegationAuditIntent{Version: 1, OperationID: operation, ReviewerID: "z"}
	base := time.Now().UTC().Add(-time.Minute)
	appendHistory := func(task string, events []runtime.Event) []runtime.Event {
		for i := range events {
			e := &events[i]
			e.Version, e.ID, e.TaskID, e.SessionID = 1, task+"-event-"+string(rune('a'+i)), task, session
			e.CorrelationID, e.Sequence, e.Time = task, int64(i+1), base.Add(time.Duration(i)*time.Millisecond)
			if err := db.AppendSubmission(ctx, int64(i), *e, submission, claim.Token); err != nil {
				t.Fatal("append recovery fixture", task, err)
			}
		}
		return events
	}
	call := providers.ToolCall{ID: "delegate-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"bounded child task","validation":"text"}`)}
	parentEvents := appendHistory(parent, []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: submission, Privacy: "local_only", ModelID: "a", ProviderID: "local", Domain: "general", Profile: "default", Messages: []providers.Message{{Role: "user", Content: "delegate"}}}},
		{Kind: runtime.TurnStarted, TurnID: "parent-turn", AttemptID: "parent-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "parent-turn", AttemptID: "parent-attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{call}, FinishReason: "tool_calls"}},
		{Kind: runtime.ToolStarted, TurnID: "parent-turn", AttemptID: "parent-attempt", Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.UncertainEffect}},
	})
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "parent-turn", AttemptID: "parent-attempt", ToolCallID: call.ID, ToolName: call.Name}
	workEvents := appendHistory(work, []runtime.Event{
		{Kind: runtime.TaskStarted, WorkerID: "worker", Data: runtime.Data{ParentTaskID: parent, SubmissionID: submission, DelegationOrigin: origin, DelegationAuditIntent: intent}},
		{Kind: runtime.WorkerStarted, WorkerID: "worker"},
	})
	accepted := true
	childEvents := appendHistory(child, []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: work, SubmissionID: submission, Privacy: "local_only", ModelID: "a", ProviderID: "local", Domain: "general", Profile: "default", Messages: []providers.Message{{Role: "user", Content: "bounded child task"}}}},
		{Kind: runtime.TurnStarted, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{Text: "durable unaccepted candidate", FinishReason: "stop"}},
		{Kind: runtime.EvaluationRecorded, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{Accepted: &accepted, Code: "deterministic.nonempty_text.v1", Validation: "text", ModelID: "a", ProviderID: "local", Domain: "general", Profile: "default"}},
		{Kind: runtime.TaskCompleted, TurnID: "child-turn", AttemptID: "child-attempt"},
	})
	request := evaluation.AuditRequest{Version: 1, IdempotencyKey: key, TaskID: child, ReviewerModelID: "z", MaxCost: 0}
	digest, err := svc.auditRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	attempt := evaluation.ReviewAttempt{Version: 1, ID: operation, TaskID: child, AttemptID: "child-attempt", ReviewerID: "z", EvaluatorModel: "z", EvaluatorProvider: "local", RequestDigest: digest, Status: "started", StartedAt: base.Add(10 * time.Millisecond)}
	if _, created, err := db.AdmitReview(ctx, attempt); err != nil || !created {
		t.Fatal("pending audit was not durably admitted", created, err)
	}
	if status, err := svc.InspectAudit(ctx, child, operation); err != nil || status.Status != "pending" {
		t.Fatal("pending audit not inspectable", status, err)
	}

	now := time.Now().UTC()
	workerPlan, err := sessions.PlanInterruptedWorker([][]runtime.Event{workEvents, childEvents}, now)
	if err != nil || len(workerPlan.Events) != 1 || workerPlan.Events[0].Kind != runtime.TaskFailed {
		t.Fatal("worker recovery did not fail closed", workerPlan, err)
	}
	if err = db.AppendSubmission(ctx, workerPlan.ExpectedSequence, workerPlan.Events[0], submission, claim.Token); err != nil {
		t.Fatal(err)
	}
	workEvents = append(workEvents, workerPlan.Events[0])
	parentPlan, err := sessions.PlanInterruptedDelegation([][]runtime.Event{parentEvents, workEvents, childEvents}, now.Add(time.Second), false)
	if err != nil || len(parentPlan.Events) != 2 || parentPlan.Events[0].Kind != runtime.ToolCompleted || parentPlan.Events[1].Kind != runtime.TaskFailed {
		t.Fatal("parent recovery did not emit bounded failure", parentPlan, err)
	}
	for _, event := range parentPlan.Events {
		if err = db.AppendSubmission(ctx, event.Sequence-1, event, submission, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	payload := parentPlan.Events[0].Data.Text
	if !strings.Contains(payload, "delegate_unavailable_or_rejected") || !strings.Contains(payload, "worker_owner_interrupted") || strings.Contains(payload, "untrusted_output") || strings.Contains(payload, `"audit"`) || strings.Contains(payload, operation) {
		t.Fatal("recovery exposed or accepted pending audit", payload)
	}
	if snapshot, err := db.TaskSnapshot(ctx, parent); err != nil || snapshot.State != "failed" {
		t.Fatal("parent recovery accepted output", snapshot, err)
	}
	status, err := svc.InspectAudit(ctx, child, operation)
	if err != nil || status.Status != "pending" {
		t.Fatal("recovery mutated or redispatched pending audit", status, err)
	}
	attempts, err := db.ReviewAttempts(ctx, child, "", 10)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
		t.Fatal("recovery created another review lifecycle", attempts, err)
	}
	if _, err := db.Audit(ctx, operation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("recovery invented accepted audit evidence", err)
	}
}
