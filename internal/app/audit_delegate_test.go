package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func auditDelegateFixture(t *testing.T, mutation string) (*Service, *telemetry.Store) {
	t.Helper()
	svc, cfg := autoFixture(t)
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seqs := map[string]int64{}
	appendEvent := func(task, session string, e runtime.Event) {
		seqs[task]++
		e.Version, e.Sequence = 1, seqs[task]
		e.TaskID, e.SessionID, e.CorrelationID = task, session, task
		e.ID = fmt.Sprintf("%s-%d", task, e.Sequence)
		e.Time = time.Now().UTC()
		if err := db.Append(context.Background(), e.Sequence-1, e); err != nil {
			t.Fatal(err)
		}
	}
	parent := []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{Domain: "code", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "Delegate and check syntax."}}}},
		{Kind: runtime.TurnStarted, TurnID: "parent-tool", AttemptID: "parent-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "parent-tool", AttemptID: "parent-attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "delegate-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"Implement","validation":"go_source"}`)}}, FinishReason: "tool_calls"}},
		{Kind: runtime.ToolStarted, TurnID: "parent-tool", AttemptID: "parent-attempt", Data: runtime.Data{ToolCallID: "delegate-call", ToolName: "delegate", Effect: runtime.NoEffect}},
	}
	for _, e := range parent {
		appendEvent("audit-parent", "parent-session", e)
	}
	owner := "audit-parent"
	if mutation == "foreign work" {
		owner = "foreign-parent"
	}
	workSession := "parent-session"
	if mutation == "foreign session" {
		workSession = "foreign-session"
	}
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "parent-tool", AttemptID: "parent-attempt", ToolCallID: "delegate-call", ToolName: "delegate"}
	switch mutation {
	case "missing origin":
		origin = nil
	case "wrong origin call":
		origin.ToolCallID = "sibling-call"
	case "wrong origin turn":
		origin.TurnID = "sibling-turn"
	case "wrong origin attempt":
		origin.AttemptID = "sibling-attempt"
	}
	appendEvent("audit-work", workSession, runtime.Event{Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: owner, Privacy: "local_only", DelegationOrigin: origin}})
	appendEvent("audit-work", workSession, runtime.Event{Kind: runtime.WorkerStarted, WorkerID: "worker-1"})
	appendEvent("audit-work", workSession, runtime.Event{Kind: runtime.TaskFailed, Data: runtime.Data{Code: "worker_failed"}})
	owner = "audit-work"
	if mutation == "foreign execution" {
		owner = "foreign-work"
	}
	accepted := false
	child := []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: owner, Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "PRIVATE_CHILD_PROMPT"}}}},
		{Kind: runtime.TurnStarted, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{ModelID: "worker", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{Text: "PRIVATE_INVALID_CHILD_OUTPUT", FinishReason: "stop"}},
		{Kind: runtime.EvaluationRecorded, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{Code: "deterministic.go_syntax.v1", Validation: "go_source", Accepted: &accepted, Text: "PRIVATE_VALIDATION_DETAIL"}},
		{Kind: runtime.TaskFailed, Data: runtime.Data{Code: "invalid_output"}},
	}
	if mutation == "wrong evaluation attempt" {
		child[3].AttemptID = "wrong"
	}
	for _, e := range child {
		appendEvent("audit-execution", "child-session", e)
	}
	report := delegateFailure{Version: 1, Error: "delegate_unavailable_or_rejected", Reason: "invalid_output", WorkID: "audit-work", ExecutionID: "audit-execution", Evidence: []delegateFailureEvidence{{TaskID: "audit-work", Sequence: 3, Kind: runtime.TaskFailed, Code: "worker_failed"}, {TaskID: "audit-execution", Sequence: 5, Kind: runtime.TaskFailed, Code: "invalid_output"}}}
	switch mutation {
	case "wrong reference":
		report.Evidence[1].Sequence = 4
	case "wrong code":
		report.Evidence[1].Code = "empty_output"
	case "wrong reason":
		report.Reason = "canceled"
	case "duplicate reference":
		report.Evidence[1] = report.Evidence[0]
	}
	body, _ := json.Marshal(report)
	switch mutation {
	case "aliases":
		body = []byte(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(string(body), `"version"`, `"Version"`), `"reason"`, `"Reason"`), `"evidence"`, `"Evidence"`))
	case "duplicate field":
		body = []byte(strings.Replace(string(body), `"version":1`, `"version":1,"version":1`, 1))
	case "generic":
		body = []byte(`{"error":"delegate_unavailable_or_rejected"}`)
	case "success":
		body = []byte(`{"work_task_id":"not-traversed","execution_task_id":"not-traversed","untrusted_output":"legacy successful output"}`)
	}
	appendEvent("audit-parent", "parent-session", runtime.Event{Kind: runtime.ToolCompleted, TurnID: "parent-tool", AttemptID: "parent-attempt", Data: runtime.Data{ToolCallID: "delegate-call", ToolName: "delegate", Effect: runtime.NoEffect, Text: string(body)}})
	appendEvent("audit-parent", "parent-session", runtime.Event{Kind: runtime.TurnStarted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}})
	appendEvent("audit-parent", "parent-session", runtime.Event{Kind: runtime.TurnCompleted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{Text: "The worker succeeded.", FinishReason: "stop"}})
	appendEvent("audit-parent", "parent-session", runtime.Event{Kind: runtime.TaskCompleted})
	return svc, db
}

func TestAuditTaskTraversesDelegatedFailureValidation(t *testing.T) {
	svc, db := auditDelegateFixture(t, "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("request decode")
			return
		}
		var envelope struct {
			Evidence []evaluation.ReviewEvidence `json:"evidence"`
		}
		for _, m := range request.Messages {
			if strings.Contains(m.Content, "PRIVATE_") {
				t.Error("raw child content exported")
			}
			if m.Role == "user" {
				if json.Unmarshal([]byte(m.Content), &envelope) != nil {
					t.Error("envelope decode")
				}
			}
		}
		found := map[string]bool{}
		for _, e := range envelope.Evidence {
			if !strings.HasPrefix(e.ID, "delegated_") {
				continue
			}
			found[e.ID] = true
			var p map[string]any
			if json.Unmarshal([]byte(e.Content), &p) != nil {
				t.Error("projection decode")
				continue
			}
			if p["parent_tool_sequence"] != float64(5) {
				t.Error("missing parent reference")
			}
			if _, ok := p["text"]; ok {
				t.Error("raw text in projection")
			}
			if e.ID == "delegated_5_execution_4" && (p["accepted"] != false || p["code"] != "deterministic.go_syntax.v1" || p["validation"] != "go_source" || p["attempt_id"] != "child-attempt") {
				t.Errorf("validator evidence: %+v", p)
			}
			if e.ID == "delegated_5_execution_4" && (p["task_id"] != "audit-execution" || p["kind"] != "evaluation.recorded" || p["sequence"] != float64(4)) {
				t.Errorf("validator attribution: %+v", p)
			}
			if e.ID == "delegated_5_work_3" && (p["task_id"] != "audit-work" || p["kind"] != "task.failed" || p["sequence"] != float64(3) || p["code"] != "worker_failed") {
				t.Errorf("work attribution: %+v", p)
			}
			if e.ID == "delegated_5_execution_5" && (p["task_id"] != "audit-execution" || p["kind"] != "task.failed" || p["sequence"] != float64(5) || p["code"] != "invalid_output") {
				t.Errorf("terminal attribution: %+v", p)
			}
		}
		for _, id := range []string{"delegated_5_work_3", "delegated_5_execution_4", "delegated_5_execution_5"} {
			if !found[id] {
				t.Errorf("missing %s", id)
			}
		}
		audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "reject", Confidence: .9, Findings: []evaluation.AuditFinding{{Summary: "The child failed Go syntax validation; the final claim lacks support.", EvidenceRefs: []string{"delegated_5_execution_4", "delegated_5_execution_5", "candidate"}}}}
		body, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	record, err := svc.AuditTask(context.Background(), "audit-parent", "z", 0)
	if err != nil || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
	saved, err := db.Audit(context.Background(), record.ID)
	if err != nil || !strings.Contains(strings.Join(saved.EvidenceRefs, ","), "delegated_5_execution_4") {
		t.Fatal("child reference not durable", err)
	}
}

func TestAuditTaskRejectsForgedDelegationBeforeDispatch(t *testing.T) {
	for _, mutation := range []string{"foreign work", "foreign session", "foreign execution", "wrong reference", "wrong code", "wrong reason", "duplicate reference", "wrong evaluation attempt", "aliases", "duplicate field", "missing origin", "wrong origin call", "wrong origin turn", "wrong origin attempt"} {
		t.Run(mutation, func(t *testing.T) {
			svc, db := auditDelegateFixture(t, mutation)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.AuditTask(context.Background(), "audit-parent", "z", 0); err == nil || calls.Load() != 0 {
				t.Fatal("forged child evidence dispatched", err, calls.Load())
			}
			attempts, err := db.ReviewAttempts(context.Background(), "audit-parent", "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatal("forgery created review attempt", err)
			}
		})
	}
}

func TestAuditDelegationSupervisorProjectionHasDistinctAttribution(t *testing.T) {
	accepted := true
	e := runtime.Event{Kind: runtime.EvaluationRecorded, TaskID: "work", Sequence: 3, WorkerID: "worker-1", Data: runtime.Data{Code: "worker_validator", Accepted: &accepted, Text: "PRIVATE_SUPERVISOR_TEXT"}}
	out, err := projectAuditChild(5, "work", []runtime.Event{e}, nil)
	if err != nil || len(out) != 1 || strings.Contains(out[0].Content, "PRIVATE_SUPERVISOR_TEXT") {
		t.Fatal("valid turnless supervisor validation rejected", err)
	}
	if _, err := projectAuditChild(5, "execution", []runtime.Event{e}, nil); err == nil {
		t.Fatal("supervisor validation treated as model-turn evidence")
	}
	for _, mutation := range []string{"code", "turn", "attempt", "worker"} {
		changed := e
		switch mutation {
		case "code":
			changed.Data.Code = "other"
		case "turn":
			changed.TurnID = "invented"
		case "attempt":
			changed.AttemptID = "invented"
		case "worker":
			changed.WorkerID = ""
		}
		if _, err := projectAuditChild(5, "work", []runtime.Event{changed}, nil); err == nil {
			t.Fatal("unbound supervisor validation accepted", mutation)
		}
	}
}

func TestAuditDelegationLegacyAndBounds(t *testing.T) {
	for _, body := range []string{`{"error":"delegate_unavailable_or_rejected"}`} {
		ref, err := auditDelegationReference(runtime.Event{Kind: runtime.ToolCompleted, Data: runtime.Data{ToolName: "delegate", Text: body, Effect: runtime.NoEffect}})
		if err != nil || ref != nil {
			t.Fatal("generic rejection traversal changed", err)
		}
	}
	if boundAuditExecutionEvidence(make([]evaluation.ReviewEvidence, 253)) || boundAuditExecutionEvidence([]evaluation.ReviewEvidence{{Content: strings.Repeat("x", (64<<10)+1)}}) {
		t.Fatal("combined evidence bound bypassed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := auditDelegatedEvidence(ctx, nil, nil, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}
