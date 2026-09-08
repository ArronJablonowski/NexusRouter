package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

type mvpAuditEnvelope struct {
	EvaluatorID string                      `json:"evaluator_id"`
	Domain      string                      `json:"domain"`
	Evidence    []evaluation.ReviewEvidence `json:"evidence"`
}

func mvpAuditRequest(r *http.Request) (string, mvpAuditEnvelope, error) {
	var request struct {
		Model    string              `json:"model"`
		Messages []providers.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return "", mvpAuditEnvelope{}, err
	}
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		var envelope mvpAuditEnvelope
		if json.Unmarshal([]byte(message.Content), &envelope) == nil && envelope.EvaluatorID != "" {
			return request.Model, envelope, nil
		}
	}
	return request.Model, mvpAuditEnvelope{}, nil
}

func mvpAuditEvidence(envelope mvpAuditEnvelope) (map[string]string, string, *bool) {
	evidence := make(map[string]string, len(envelope.Evidence))
	evaluationRef := ""
	var evaluationAccepted *bool
	for _, item := range envelope.Evidence {
		evidence[item.ID] = item.Content
		if !strings.HasPrefix(item.ID, "execution_") {
			continue
		}
		var projection struct {
			Kind     runtime.Kind `json:"kind"`
			Code     string       `json:"code"`
			Accepted *bool        `json:"accepted"`
		}
		if json.Unmarshal([]byte(item.Content), &projection) == nil && projection.Kind == runtime.EvaluationRecorded && projection.Code == "deterministic.go_syntax.v1" && projection.Accepted != nil {
			evaluationRef = item.ID
			accepted := *projection.Accepted
			evaluationAccepted = &accepted
		}
	}
	return evidence, evaluationRef, evaluationAccepted
}

func mvpAuditToolEvidence(envelope mvpAuditEnvelope) (map[string]string, string) {
	evidence := make(map[string]string, len(envelope.Evidence))
	toolRef := ""
	for _, item := range envelope.Evidence {
		evidence[item.ID] = item.Content
		if !strings.HasPrefix(item.ID, "execution_") {
			continue
		}
		var projection struct {
			Kind         runtime.Kind         `json:"kind"`
			ToolName     string               `json:"tool_name"`
			ToolBehavior runtime.ToolBehavior `json:"tool_behavior"`
			Effect       runtime.Effect       `json:"effect"`
		}
		if json.Unmarshal([]byte(item.Content), &projection) == nil && projection.Kind == runtime.ToolCompleted && projection.ToolName == "lookup" && projection.ToolBehavior == runtime.BehaviorReadOnly && projection.Effect == runtime.NoEffect {
			toolRef = item.ID
		}
	}
	return evidence, toolRef
}

// This qualification deliberately makes the model auditor accept output that
// the production Go validator rejected. The audit remains durable and
// inspectable, but the evidence resolver and fitness aggregate must continue to
// follow the actual persisted validator event instead of the orchestrator.
func TestMVPObjectiveCodeAuditEvidencePrecedence(t *testing.T) {
	const candidate = "package answer\nfunc Add(a, b int) int { return a + }"
	ctx := context.Background()
	svc, cfg := autoFixture(t)

	var mu sync.Mutex
	reviewerEvidence := map[string]string{}
	reviewerEvaluationRef := ""
	var reviewerEvaluationAccepted *bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		model, envelope, err := mvpAuditRequest(r)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if envelope.EvaluatorID == "" {
			if model != "a" {
				t.Errorf("candidate dispatched to %q", model)
			}
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", candidate)
			return
		}
		evidence, evaluationRef, evaluationAccepted := mvpAuditEvidence(envelope)
		mu.Lock()
		reviewerEvidence, reviewerEvaluationRef, reviewerEvaluationAccepted = evidence, evaluationRef, evaluationAccepted
		mu.Unlock()
		// The acceptance is intentionally contradicted by the syntax event.
		audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "accept", Confidence: .95, Findings: []evaluation.AuditFinding{{Summary: "The implementation is valid Go and implements addition.", EvidenceRefs: []string{"candidate", "candidate_execution", evaluationRef}}}}
		body, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	result, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Return a complete Go file defining Add so it returns a+b.", Domain: "code", Validation: "go_source"})
	if !errors.Is(err, runtime.ErrInvalidOutput) || result.TaskID == "" || result.Text != "" {
		t.Fatalf("objective candidate failed: %+v %v", result, err)
	}
	record, err := svc.AuditTask(ctx, result.TaskID, "z", 0)
	if err != nil || record.Audit.Verdict != "accept" || record.Audit.EvaluatorID != "z" || record.EvaluatorModel != "z" || record.EvaluatorProvider != "local" || record.TaskID != result.TaskID || record.AttemptID == "" {
		t.Fatalf("typed audit identity or disposition missing: %+v %v", record, err)
	}
	mu.Lock()
	evidence := reviewerEvidence
	evaluationRef := reviewerEvaluationRef
	evaluationAccepted := reviewerEvaluationAccepted
	mu.Unlock()
	if evidence["candidate"] != candidate || evidence["candidate_execution"] == "" || evaluationRef == "" || evaluationAccepted == nil || *evaluationAccepted || !reflect.DeepEqual(record.Audit.Findings[0].EvidenceRefs, []string{"candidate", "candidate_execution", evaluationRef}) {
		t.Fatalf("audit did not cite durable candidate/evaluation evidence: refs=%v evidence=%v", record.Audit.Findings[0].EvidenceRefs, evidence)
	}

	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.Audit(ctx, record.ID)
	if err != nil || !reflect.DeepEqual(saved, record) {
		t.Fatalf("audit identity, source, findings, or disposition not durable: %+v %v", saved, err)
	}
	validityKey := routing.Key{Model: "a", Provider: "local", Domain: "code", Profile: "default"}
	validity, err := db.OutputValidity(ctx, validityKey, "go_source")
	if err != nil || validity.Samples != 1 || validity.Failures != 1 {
		t.Fatalf("contradictory audit altered deterministic validity: %+v %v", validity, err)
	}
	var validationEvent runtime.Event
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == runtime.EvaluationRecorded && event.Data.Code == "deterministic.go_syntax.v1" {
			validationEvent = event
		}
	}
	if validationEvent.ID == "" || validationEvent.AttemptID != record.AttemptID || validationEvent.Data.Accepted == nil || *validationEvent.Data.Accepted || evaluationRef != fmt.Sprintf("execution_%d", validationEvent.Sequence) {
		t.Fatalf("review did not cite the persisted failed validator: event=%+v ref=%q", validationEvent, evaluationRef)
	}

	// A trusted evaluator may include the audit as lower-priority context, but
	// Resolve must select the deterministic check and persist that disposition.
	schemaPassed := false
	objective := evaluation.Record{Version: 1, ID: "mvp-objective-code-review", TaskID: result.TaskID, AttemptID: record.AttemptID, Key: validityKey, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: validationEvent.ID, Passed: false}, {Source: evaluation.LLMJudge, Reference: record.ID, Passed: true}}, AllowJudge: true, SchemaPassed: &schemaPassed, ExecutionSucceeded: false, Time: time.Now().UTC()}
	resolved, err := evaluation.Resolve(objective.Checks, objective.AllowJudge)
	if err != nil || resolved.Source != evaluation.Deterministic || resolved.Accepted || !reflect.DeepEqual(resolved.References, []string{validationEvent.ID}) {
		t.Fatalf("evidence precedence failed: %+v %v", resolved, err)
	}
	if err := db.RecordEvaluation(ctx, objective); err != nil {
		t.Fatal(err)
	}
	fitness, err := db.Fitness(ctx, validityKey)
	if err != nil || fitness.Samples != 1 || fitness.Quality != 0 || fitness.Compliance != 0 || fitness.Reliability != 0 {
		t.Fatalf("audit overrode objective fitness: %+v %v", fitness, err)
	}
	advisory, err := db.AuditQuality(ctx, validityKey)
	if err != nil || advisory != (routing.Advisory{}) {
		t.Fatalf("lower-priority audit remained routable after objective evidence: %+v %v", advisory, err)
	}
}

// The tool-result case uses an actual production tool turn and its immutable
// ToolCompleted event as the evidence reference. A contrary audit acceptance is
// retained for inspection but cannot turn the tool-grounded failure disposition
// into success.
func TestMVPReadOnlyToolEvidencePrecedesAuditOpinion(t *testing.T) {
	ctx := context.Background()
	fixture, cfg := autoFixture(t)
	extension, err := tools.NewExtension([]tools.Definition{{
		Tool:     providers.Tool{Name: "lookup", Description: "Look up the fixture record.", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
		Scope:    "fixture",
		ReadOnly: true,
		Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: "record exists", Effect: runtime.NoEffect}, nil
		},
	}}, &tools.Policy{Default: tools.Allow})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	modelCalls := 0
	reviewerEvidence := map[string]string{}
	reviewerToolRef := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		model, envelope, decodeErr := mvpAuditRequest(r)
		if decodeErr != nil {
			t.Error(decodeErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if envelope.EvaluatorID != "" {
			evidence, toolRef := mvpAuditToolEvidence(envelope)
			mu.Lock()
			reviewerEvidence, reviewerToolRef = evidence, toolRef
			mu.Unlock()
			audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "accept", Confidence: .95, Findings: []evaluation.AuditFinding{{Summary: "The candidate accurately reports that no record exists.", EvidenceRefs: []string{"candidate", "candidate_execution", toolRef}}}}
			body, _ := json.Marshal(audit)
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
			return
		}
		if model != "a" {
			t.Errorf("task dispatched to %q", model)
		}
		mu.Lock()
		modelCalls++
		call := modelCalls
		mu.Unlock()
		if call == 1 {
			fmt.Fprintln(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":true,"done_reason":"stop"}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"No records exist."},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	cfg.Providers[0].Endpoint = server.URL
	svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, nil, extension)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile

	result, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Use lookup and report whether the record exists.", Domain: "code"})
	if err != nil || result.Text != "No records exist." || result.Turns != 2 {
		t.Fatalf("production read-only tool turn failed: %+v %v", result, err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, result.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var toolEvent runtime.Event
	for _, event := range events {
		if event.Kind == runtime.ToolCompleted && event.Data.ToolName == "lookup" {
			toolEvent = event
		}
	}
	if toolEvent.ID == "" || toolEvent.Data.ToolBehavior != runtime.BehaviorReadOnly || toolEvent.Data.Effect != runtime.NoEffect || toolEvent.Data.Text != "record exists" {
		t.Fatalf("read-only ToolCompleted evidence not durable: %+v", toolEvent)
	}

	record, err := svc.AuditTask(ctx, result.TaskID, "z", 0)
	if err != nil || record.Audit.Verdict != "accept" || record.AttemptID == "" {
		t.Fatalf("contradictory tool audit failed: %+v %v", record, err)
	}
	mu.Lock()
	evidence, toolRef := reviewerEvidence, reviewerToolRef
	mu.Unlock()
	if toolRef != fmt.Sprintf("execution_%d", toolEvent.Sequence) || !strings.Contains(evidence["session_history"], "record exists") || !reflect.DeepEqual(record.Audit.Findings[0].EvidenceRefs, []string{"candidate", "candidate_execution", toolRef}) {
		t.Fatalf("audit did not cite durable tool evidence: ref=%q event=%+v evidence=%v", toolRef, toolEvent, evidence)
	}

	key := routing.Key{Model: "a", Provider: "local", Domain: "code", Profile: "default"}
	objective := evaluation.Record{Version: 1, ID: "mvp-tool-result-review", TaskID: result.TaskID, AttemptID: record.AttemptID, Key: key, Checks: []evaluation.Check{{Source: evaluation.ToolResult, Reference: toolEvent.ID, Passed: false}, {Source: evaluation.LLMJudge, Reference: record.ID, Passed: true}}, AllowJudge: true, ExecutionSucceeded: true, Time: time.Now().UTC()}
	resolved, err := evaluation.Resolve(objective.Checks, objective.AllowJudge)
	if err != nil || resolved.Source != evaluation.ToolResult || resolved.Accepted || !reflect.DeepEqual(resolved.References, []string{toolEvent.ID}) {
		t.Fatalf("durable tool-result precedence failed: %+v %v", resolved, err)
	}
	if err := db.RecordEvaluation(ctx, objective); err != nil {
		t.Fatal(err)
	}
	fitness, err := db.Fitness(ctx, key)
	if err != nil || fitness.Samples != 1 || fitness.Quality != 0 || fitness.Reliability != 1 {
		t.Fatalf("audit overrode durable tool-result fitness: %+v %v", fitness, err)
	}
	advisory, err := db.AuditQuality(ctx, key)
	if err != nil || advisory != (routing.Advisory{}) {
		t.Fatalf("audit remained routable after stronger tool evidence: %+v %v", advisory, err)
	}
}

// Automatic auditing must run only after route-chain fallback succeeds. A
// failed predecessor is durable for diagnosis, but is not silently submitted
// to the reviewer as though it were the final candidate.
func TestMVPAuditRunsOnlyOnSuccessfulFallback(t *testing.T) {
	const fallback = "package answer\nfunc Add(a, b int) int { return a + b }"
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	reviewer := svc.settings.Models[0]
	reviewer.ID, reviewer.Model = "reviewer", "reviewer"
	reviewer.Capabilities = []string{"audit"}
	reviewer.FailureDomain = "reviewer"
	svc.settings.Models = append(svc.settings.Models, reviewer)
	svc.settings.Models[0].FailureDomain = "primary"
	svc.settings.Models[1].FailureDomain = "fallback"
	svc.settings.Evaluation.AutoReviewModel = reviewer.ID
	svc.settings.Evaluation.AutoReviewMaxCost = 0

	var mu sync.Mutex
	inferenceCalls := []string{}
	auditCandidate, auditEvaluationRef := "", ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"},{"name":"reviewer"}]}`)
			return
		}
		model, envelope, err := mvpAuditRequest(r)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if envelope.EvaluatorID == "" {
			mu.Lock()
			inferenceCalls = append(inferenceCalls, model)
			mu.Unlock()
			if model == "a" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", fallback)
			return
		}
		evidence, evaluationRef, evaluationAccepted := mvpAuditEvidence(envelope)
		if evaluationAccepted == nil || !*evaluationAccepted {
			t.Error("fallback reviewer lacked accepted Go validation evidence")
		}
		mu.Lock()
		auditCandidate, auditEvaluationRef = evidence["candidate"], evaluationRef
		mu.Unlock()
		audit := evaluation.Audit{Version: 1, EvaluatorID: reviewer.ID, RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "accept", Confidence: .9, Findings: []evaluation.AuditFinding{{Summary: "The final fallback implements the requested addition.", EvidenceRefs: []string{"candidate", "candidate_execution", evaluationRef}}}}
		body, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	out, err := svc.Run(ctx, Request{Prompt: "Return a complete Go file defining Add so it returns a+b.", Domain: "code", Validation: "go_source", Capabilities: []string{"chat"}})
	if err != nil || out.Text != fallback || len(out.PreviousTaskIDs) != 1 || out.AuditStatus != "recorded" || out.AuditID == "" {
		t.Fatalf("fallback was not followed by a successful audit: %+v %v", out, err)
	}
	mu.Lock()
	calls := append([]string(nil), inferenceCalls...)
	candidate, evaluationRef := auditCandidate, auditEvaluationRef
	mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"a", "z"}) || candidate != fallback || evaluationRef == "" {
		t.Fatalf("reviewer did not receive only final fallback evidence: calls=%v candidate=%q evaluation=%q", calls, candidate, evaluationRef)
	}

	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	failedAttempts, err := db.ReviewAttempts(ctx, out.PreviousTaskIDs[0], "", 100)
	if err != nil || len(failedAttempts) != 0 {
		t.Fatalf("failed predecessor was audited: %+v %v", failedAttempts, err)
	}
	finalAttempts, err := db.ReviewAttempts(ctx, out.TaskID, "", 100)
	if err != nil || len(finalAttempts) != 1 || finalAttempts[0].Status != "completed" || finalAttempts[0].AuditID != out.AuditID {
		t.Fatalf("final fallback audit lifecycle missing: %+v %v", finalAttempts, err)
	}
	record, err := db.Audit(ctx, out.AuditID)
	if err != nil || record.TaskID != out.TaskID || record.AttemptID != finalAttempts[0].AttemptID || record.EvaluatorModel != reviewer.Model || record.EvaluatorProvider != reviewer.Provider || record.Audit.Verdict != "accept" || !reflect.DeepEqual(record.Audit.Findings[0].EvidenceRefs, []string{"candidate", "candidate_execution", evaluationRef}) {
		t.Fatalf("final fallback audit attribution not durable: %+v %v", record, err)
	}
}
