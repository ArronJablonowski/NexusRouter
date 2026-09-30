package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestDelegateSingleAndBatchPublishSanitizedAudits(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "delegated-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Defaults()
	cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 3, 3
	cfg.Evaluation.Judge, cfg.Evaluation.AutoReviewModel = true, "reviewer"
	registry := &tools.Registry{}
	var reviews atomic.Int32
	audit := func(_ context.Context, workID, executionID string) (*runtime.DelegationAudit, error) {
		reviews.Add(1)
		confidence := .75
		if !strings.HasPrefix(executionID, "execution-") {
			t.Error("review was not bound to execution", executionID)
		}
		return &runtime.DelegationAudit{Version: 1, OperationID: auditOperationID(delegationAuditKey(workID)), ReviewerID: "reviewer", AuditID: "audit-" + workID, Status: "completed", Verdict: "accept", Confidence: &confidence, Citations: []string{"candidate"}}, nil
	}
	run := func(_ context.Context, prompt, _ string, workID string, _ bool) (Result, error) {
		if prompt == "invalid" {
			return Result{TaskID: "execution-" + workID, Text: " "}, nil
		}
		return Result{TaskID: "execution-" + workID, Text: "answer-" + prompt}, nil
	}
	if err = registerDelegate(registry, nil, db, db, cfg, "parent", "session", "", true, run, audit, nil); err != nil {
		t.Fatal(err)
	}
	executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: applicationToolPolicy()}}
	single, err := executor.Execute(ctx, providers.ToolCall{ID: "single", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"one","validation":"text"}`)})
	if err != nil || single.Failed {
		t.Fatal(single, err)
	}
	var item struct {
		WorkID string                  `json:"work_task_id"`
		Output string                  `json:"untrusted_output"`
		Audit  delegateAuditProjection `json:"audit"`
	}
	if json.Unmarshal([]byte(single.Content), &item) != nil || item.Output != "answer-one" || item.Audit.Status != "completed" || item.Audit.Verdict != "accept" || item.Audit.Confidence == nil || *item.Audit.Confidence != .75 || len(item.Audit.Citations) != 1 || strings.Contains(single.Content, "operation_id") || strings.Contains(single.Content, "audit_id") {
		t.Fatal(single.Content)
	}
	events, err := db.Read(ctx, item.WorkID, 0, 100)
	if err != nil || events[0].Data.DelegationAuditIntent == nil {
		t.Fatal(events, err)
	}
	var workerAudit *runtime.DelegationAudit
	for _, event := range events {
		if event.Kind == runtime.WorkerCompleted {
			workerAudit = event.Data.DelegationAudit
		}
	}
	if workerAudit == nil || workerAudit.Status != "completed" {
		t.Fatal("durable audit missing", events)
	}

	batch, err := executor.Execute(ctx, providers.ToolCall{ID: "batch", Name: "delegate_batch", Arguments: json.RawMessage(`{"tasks":[{"prompt":"two","validation":"text"},{"prompt":"three","validation":"text"}]}`)})
	if err != nil || batch.Failed || reviews.Load() != 3 {
		t.Fatal(batch, err, reviews.Load())
	}
	var envelope struct {
		Results []struct {
			Output string                   `json:"untrusted_output"`
			Audit  *delegateAuditProjection `json:"audit"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(batch.Content), &envelope) != nil || len(envelope.Results) != 2 || envelope.Results[0].Output != "answer-two" || envelope.Results[1].Output != "answer-three" || envelope.Results[0].Audit == nil || envelope.Results[1].Audit == nil {
		t.Fatal(batch.Content)
	}
}

func TestDelegateNeverAuditsInvalidChild(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "invalid-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Defaults()
	cfg.Workers.DelegateMaxCalls = 1
	cfg.Evaluation.Judge, cfg.Evaluation.AutoReviewModel = true, "reviewer"
	registry := &tools.Registry{}
	var reviews atomic.Int32
	if err = registerDelegate(registry, nil, db, db, cfg, "parent", "session", "", true,
		func(_ context.Context, _, _ string, workID string, _ bool) (Result, error) {
			return Result{TaskID: "execution-" + workID, Text: " "}, nil
		}, func(context.Context, string, string) (*runtime.DelegationAudit, error) {
			reviews.Add(1)
			return nil, ErrAdmission
		}, nil); err != nil {
		t.Fatal(err)
	}
	executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: applicationToolPolicy()}}
	out, err := executor.Execute(ctx, providers.ToolCall{ID: "invalid", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"invalid","validation":"text"}`)})
	if reviews.Load() != 0 || (!out.Failed && err == nil) || strings.Contains(out.Content, "invalid") {
		t.Fatal(out, err, reviews.Load())
	}
}

func TestDelegationAuditOperationExactReplayFailureAndPolicyDenial(t *testing.T) {
	ctx := context.Background()
	svc, request := auditOperationSource(t)
	svc.settings.Evaluation.Judge = true
	svc.settings.Evaluation.AutoReviewModel = "z"
	var calls atomic.Int32
	mode := "valid"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if mode == "provider_failure" {
			http.Error(w, "private provider failure", http.StatusInternalServerError)
			return
		}
		if mode == "malformed" {
			fmt.Fprintln(w, `{"message":{"content":"private malformed review"},"done":true,"done_reason":"stop"}`)
			return
		}
		auditResponse(t, w, r, "The child output is responsive.")
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	runner := svc.bindDelegationAudit()
	first, err := runner(ctx, "work-exact", request.TaskID)
	second, replayErr := runner(ctx, "work-exact", request.TaskID)
	if err != nil || replayErr != nil || calls.Load() != 1 || first.Status != "completed" || second.Status != "completed" || first.OperationID != second.OperationID || first.Confidence == nil || len(first.Citations) != 1 {
		t.Fatal(first, second, err, replayErr, calls.Load())
	}

	mode = "malformed"
	malformed, err := runner(ctx, "work-malformed", request.TaskID)
	if err != nil || malformed.Status != "failed" || malformed.Confidence != nil || len(malformed.Citations) != 0 || calls.Load() != 2 {
		t.Fatal(malformed, err, calls.Load())
	}
	mode = "provider_failure"
	failed, err := runner(ctx, "work-provider-failure", request.TaskID)
	if err != nil || failed.Status != "failed" || calls.Load() != 3 {
		t.Fatal(failed, err, calls.Load())
	}

	// The completed source is local-only; a cloud reviewer is rejected before
	// provider dispatch and is projected without exposing the admission reason.
	svc.settings.Mode = "hybrid"
	for i := range svc.settings.Models {
		if svc.settings.Models[i].ID == "z" {
			svc.settings.Models[i].Locality = "cloud"
		}
	}
	denied, err := runner(ctx, "work-policy-denied", request.TaskID)
	if err != nil || denied.Status != "not_run" || calls.Load() != 3 {
		t.Fatal(denied, err, calls.Load())
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := runner(canceled, "work-canceled", request.TaskID); err == nil || calls.Load() != 3 {
		t.Fatal("canceled review was admitted", err, calls.Load())
	}

	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempts, err := db.ReviewAttempts(ctx, request.TaskID, "", 100)
	if err != nil || len(attempts) != 3 {
		t.Fatal("unexpected durable review attempts", attempts, err)
	}
	for _, attempt := range attempts {
		if attempt.ReviewerID != "z" {
			t.Fatal("delegated audit lost orchestrator identity", attempt)
		}
	}
}

func TestDelegationAuditSameModelAcceptanceCannotBoostFitness(t *testing.T) {
	ctx := context.Background()
	svc, request := auditOperationSource(t)
	svc.settings.Evaluation.Judge = true
	svc.settings.Evaluation.AutoReviewModel = "a"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "a" {
			t.Error("unexpected same-model review")
			return
		}
		audit := evaluation.Audit{Version: 1, EvaluatorID: "a", RubricVersion: "darwin-review-v2", Domain: "general", Verdict: "accept", Confidence: 1, Findings: []evaluation.AuditFinding{{Summary: "Self approval.", EvidenceRefs: []string{"candidate"}}}}
		encoded, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", encoded)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	result, err := svc.bindDelegationAudit()(ctx, "work-self-review", request.TaskID)
	if err != nil || result.Status != "completed" {
		t.Fatal(result, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	observations, err := db.ObservationSet(ctx, routing.Key{Model: "a", Provider: "local", Domain: "general", Profile: "default"})
	if err != nil || len(observations.Advisory) != 0 {
		t.Fatal("same-model approval affected routing", observations.Advisory, err)
	}
}
