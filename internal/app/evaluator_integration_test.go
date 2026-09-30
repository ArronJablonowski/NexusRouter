package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type applicationEvaluator struct {
	descriptor evaluation.EvaluatorDescriptor
	calls      atomic.Int32
	evaluate   func(context.Context, evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error)
}

func (e *applicationEvaluator) Descriptor() evaluation.EvaluatorDescriptor { return e.descriptor }

func (e *applicationEvaluator) Evaluate(ctx context.Context, request evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
	e.calls.Add(1)
	return e.evaluate(ctx, request)
}

func acceptedEvaluatorResponse(descriptor evaluation.EvaluatorDescriptor, domain string) evaluation.EvaluatorResponse {
	return evaluation.EvaluatorResponse{Version: evaluation.EvaluatorContractVersion, Audit: evaluation.Audit{
		Version:       1,
		EvaluatorID:   descriptor.ID,
		RubricVersion: descriptor.RubricVersion,
		Domain:        domain,
		Verdict:       "accept",
		Confidence:    1,
		Findings:      []evaluation.AuditFinding{{Summary: "The durable candidate evidence supports the requested result.", EvidenceRefs: []string{"candidate_execution"}}},
	}}
}

func TestInjectedEvaluatorRunsAfterDurableAdmissionWithoutProviderConstruction(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Return exactly a.", Domain: "general"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := evaluation.EvaluatorDescriptor{Version: evaluation.EvaluatorContractVersion, ID: "z", Revision: "deterministic-v1", RubricVersion: "fixture-rubric-v1"}
	evaluator := &applicationEvaluator{descriptor: descriptor}
	evaluator.evaluate = func(_ context.Context, request evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
		if request.Validate() != nil || request.Domain != "general" || request.Candidate != "a" {
			t.Fatal("unvalidated evaluator request", request)
		}
		seenHistory, seenCandidate := false, false
		for _, item := range request.Evidence {
			seenHistory = seenHistory || item.ID == "session_history"
			seenCandidate = seenCandidate || item.ID == "candidate_execution"
		}
		if !seenHistory || !seenCandidate {
			t.Fatal("assembled evidence missing", request.Evidence)
		}
		db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer db.Close()
		attempts, readErr := db.ReviewAttempts(ctx, source.TaskID, "", 100)
		if readErr != nil || len(attempts) != 1 || attempts[0].Status != "started" || attempts[0].EvaluatorModel != descriptor.Revision || attempts[0].EvaluatorProvider != evaluation.EvaluatorExtensionProvider || attempts[0].EstimatedCost != 0 {
			t.Fatal("evaluation preceded durable extension admission", attempts, readErr)
		}
		return acceptedEvaluatorResponse(descriptor, request.Domain), nil
	}
	svc.evaluator = evaluator
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		t.Fatal("injected evaluation constructed a provider")
		return nil, errors.New("unreachable")
	})
	svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) {
		t.Fatal("injected evaluation used the provider context estimator")
		return 0, errors.New("unreachable")
	})
	for i := range svc.settings.Models {
		if svc.settings.Models[i].ID == "z" {
			svc.settings.Models[i].ContextTokens = 0
			svc.settings.Models[i].EstimatedCost = nil
			svc.settings.Models[i].RAMBytes = 0
		}
	}
	svc.settings.Providers[0].APIKeyEnv = "MISSING_REVIEWER_KEY"

	record, err := svc.AuditTask(ctx, source.TaskID, "z", 0)
	if err != nil || evaluator.calls.Load() != 1 || record.EvaluatorModel != descriptor.Revision || record.EvaluatorProvider != evaluation.EvaluatorExtensionProvider || record.Audit.EvaluatorID != descriptor.ID || record.Audit.RubricVersion != descriptor.RubricVersion || record.Usage != nil || record.Elapsed <= 0 {
		t.Fatal("injected evaluation failed", record, err, evaluator.calls.Load())
	}
}

func TestInjectedEvaluatorPublicOperationDispatchesOnceAndReplaysAfterRestart(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Return exactly a.", Domain: "general"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := evaluation.EvaluatorDescriptor{Version: evaluation.EvaluatorContractVersion, ID: "z", Revision: "deterministic-v1", RubricVersion: "fixture-rubric-v1"}
	entered, release := make(chan struct{}), make(chan struct{})
	evaluator := &applicationEvaluator{descriptor: descriptor}
	evaluator.evaluate = func(_ context.Context, request evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
		close(entered)
		<-release
		return acceptedEvaluatorResponse(descriptor, request.Domain), nil
	}
	svc.evaluator = evaluator
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		t.Fatal("injected evaluation constructed a provider")
		return nil, errors.New("unreachable")
	})
	request := evaluation.AuditRequest{Version: 1, IdempotencyKey: "injected-evaluator-operation", TaskID: source.TaskID, ReviewerModelID: descriptor.ID}
	type outcome struct {
		status evaluation.AuditStatus
		err    error
	}
	first := make(chan outcome, 1)
	go func() {
		status, runErr := svc.RunAudit(ctx, request, func(evaluation.AuditEvent) error { return nil })
		first <- outcome{status, runErr}
	}()
	<-entered
	pending, err := svc.RunAudit(ctx, request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || pending.Status != "pending" || evaluator.calls.Load() != 1 {
		t.Fatal("concurrent replay duplicated evaluator", pending, err, evaluator.calls.Load())
	}
	close(release)
	completed := <-first
	if completed.err != nil || completed.status.Status != "completed" || completed.status.EvaluatorModel != descriptor.Revision || completed.status.EvaluatorProvider != evaluation.EvaluatorExtensionProvider || evaluator.calls.Load() != 1 {
		t.Fatal("owning evaluation did not complete", completed, evaluator.calls.Load())
	}

	restarted, err := NewServiceWithContextEstimatorAndEvaluator(cfg, nil, nil, nil, nil, applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		t.Fatal("restart replay constructed a provider")
		return nil, errors.New("unreachable")
	}), nil, nil, nil, nil, evaluator)
	if err != nil {
		t.Fatal(err)
	}
	var events []evaluation.AuditEvent
	replayed, err := restarted.RunAudit(ctx, request, func(event evaluation.AuditEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || replayed.Status != "completed" || len(events) != 2 || evaluator.calls.Load() != 1 {
		t.Fatal("restart did not replay durable evaluation", replayed, events, err, evaluator.calls.Load())
	}
}

func TestInjectedEvaluatorFailureIsDurableSanitizedAndNotReinvoked(t *testing.T) {
	svc, _ := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Return exactly a.", Domain: "general"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := evaluation.EvaluatorDescriptor{Version: evaluation.EvaluatorContractVersion, ID: "z", Revision: "failing-v1", RubricVersion: "fixture-rubric-v1"}
	evaluator := &applicationEvaluator{descriptor: descriptor, evaluate: func(context.Context, evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
		return evaluation.EvaluatorResponse{}, errors.New("private evaluator failure")
	}}
	svc.evaluator = evaluator
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		t.Fatal("failed injected evaluation constructed a provider")
		return nil, errors.New("unreachable")
	})
	request := evaluation.AuditRequest{Version: 1, IdempotencyKey: "failing-evaluator-operation", TaskID: source.TaskID, ReviewerModelID: descriptor.ID}
	var events []evaluation.AuditEvent
	status, err := svc.RunAudit(ctx, request, func(event evaluation.AuditEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, ErrAuditOperation) || status.Status != "failed" || status.ErrorCode != "review_failed" || len(events) != 2 || evaluator.calls.Load() != 1 || containsError(err, "private evaluator failure") {
		t.Fatal("extension failure was not durably sanitized", status, events, err, evaluator.calls.Load())
	}
	status, err = svc.RunAudit(ctx, request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "failed" || evaluator.calls.Load() != 1 {
		t.Fatal("failed operation was reinvoked", status, err, evaluator.calls.Load())
	}
}

func containsError(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}
