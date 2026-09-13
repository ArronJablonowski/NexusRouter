package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type workboardEvaluationEvents struct {
	events []runtime.Event
	err    error
}

func (s workboardEvaluationEvents) Read(context.Context, string, int64, int) ([]runtime.Event, error) {
	return append([]runtime.Event(nil), s.events...), s.err
}

type hostileBudgetedWorkboardReviewer struct {
	result workboard.BudgetedCandidateEvaluation
}

func (*hostileBudgetedWorkboardReviewer) EvaluateCandidate(context.Context, workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	return nil, ErrAdmission
}

func (*hostileBudgetedWorkboardReviewer) AuxiliaryReviewReservation(workboard.CandidateEvaluationRequest) (workboard.AuxiliaryReviewReservation, error) {
	return workboard.AuxiliaryReviewReservation{}, nil
}

func (r *hostileBudgetedWorkboardReviewer) EvaluateBudgetedCandidate(context.Context, workboard.CandidateEvaluationRequest) (workboard.BudgetedCandidateEvaluation, error) {
	return r.result, nil
}

func TestConfiguredWorkboardCandidateEvaluatorCombinesTrustedAndAdvisoryEvidence(t *testing.T) {
	frozen := workboardReviewFrozen(t)
	frozen.Criteria[0].ValidatorID = meaningfulWorkboardOutputValidator
	frozen.CriteriaDigest = workboard.AcceptanceCriteriaDigest(frozen.Criteria)
	provider := &workboardReviewProvider{response: workboardAuditResponse("reject", []any{
		map[string]any{"summary": "The objective claim is unsupported.", "evidence_refs": []string{"criterion_00"}},
	})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.newID = func() string { return "advisory-audit" }
	evaluator, err := newConfiguredWorkboardCandidateEvaluator(workboardEvaluationEvents{}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.(workboard.BudgetedCandidateEvaluator).EvaluateBudgetedCandidate(context.Background(), frozen)
	if err != nil || len(result.Evidence) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	deterministic, advisory := result.Evidence[0], result.Evidence[1]
	if deterministic.Source != "deterministic" || deterministic.Outcome != "passed" ||
		deterministic.ActorID != meaningfulWorkboardOutputValidator || deterministic.Reference != frozen.SourceCompletionEventID {
		t.Fatalf("deterministic=%+v", deterministic)
	}
	if advisory.Source != "model_audit" || advisory.Outcome != "failed" || advisory.Reference != result.Audit.ID {
		t.Fatalf("advisory=%+v", advisory)
	}
	if workboard.ValidateBudgetedCandidateEvidence(frozen, result.Audit, result.Evidence) != nil {
		t.Fatal("combined evidence failed validation")
	}
}

func TestConfiguredWorkboardCandidateEvaluatorRunsExactRegisteredValidator(t *testing.T) {
	frozen := workboardReviewFrozen(t)
	registry, err := workboard.NewCandidateValidatorRegistry(map[string]workboard.CandidateValidator{
		frozen.Criteria[0].ValidatorID: workboard.CandidateValidatorFunc(func(_ context.Context, input workboard.CandidateValidationInput) (workboard.CandidateValidationDecision, error) {
			if input.Candidate.CandidateDigest != frozen.CandidateDigest || input.Criterion != frozen.Criteria[0] {
				t.Fatal("validator received a different candidate binding")
			}
			return workboard.CandidateValidationDecision{Passed: true, Reference: "trusted-validator-event"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.newID = func() string { return "runtime-projection-audit" }
	evaluator, err := newConfiguredWorkboardCandidateEvaluator(workboardEvaluationEvents{}, reviewer, registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.(workboard.BudgetedCandidateEvaluator).EvaluateBudgetedCandidate(context.Background(), frozen)
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].ActorID != frozen.Criteria[0].ValidatorID ||
		result.Evidence[0].Reference != "trusted-validator-event" || result.Evidence[0].Outcome != "passed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfiguredWorkboardCandidateEvaluatorProjectsExactRuntimeValidator(t *testing.T) {
	frozen := workboardReviewFrozen(t)
	frozen.SourceTerminalSequence++
	accepted := true
	events := workboardEvaluationEvents{events: []runtime.Event{{
		Version: 1, ID: "trusted-validator-event", TaskID: frozen.SourceTaskID, SessionID: frozen.SourceSessionID,
		Sequence: frozen.SourceCompletionSequence + 1, TurnID: frozen.SourceTurnID, AttemptID: frozen.SourceAttemptID, Kind: runtime.EvaluationRecorded,
		Data: runtime.Data{Code: frozen.Criteria[0].ValidatorID, Accepted: &accepted},
	}}}
	provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.newID = func() string { return "runtime-projection-audit" }
	evaluator, err := newConfiguredWorkboardCandidateEvaluator(events, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.(workboard.BudgetedCandidateEvaluator).EvaluateBudgetedCandidate(context.Background(), frozen)
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].ActorID != frozen.Criteria[0].ValidatorID ||
		result.Evidence[0].Reference != "trusted-validator-event" || result.Evidence[0].Outcome != "passed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfiguredWorkboardCandidateEvaluatorFailsClosedForUnconfiguredRequiredValidator(t *testing.T) {
	frozen := workboardReviewFrozen(t)
	provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	reviewer.newID = func() string { return "identity-audit" }
	evaluator, err := newConfiguredWorkboardCandidateEvaluator(workboardEvaluationEvents{}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = evaluator.(workboard.BudgetedCandidateEvaluator).EvaluateBudgetedCandidate(context.Background(), frozen); !errors.Is(err, ErrAdmission) {
		t.Fatalf("unconfigured required validator admitted: %v", err)
	}
	if provider.calls.Load() != 0 {
		t.Fatal("reviewer ran before required deterministic validator was resolved")
	}
}

func TestConfiguredWorkboardCandidateEvaluatorContainsValidatorPanicBeforeReview(t *testing.T) {
	frozen := workboardReviewFrozen(t)
	registry, err := workboard.NewCandidateValidatorRegistry(map[string]workboard.CandidateValidator{
		frozen.Criteria[0].ValidatorID: workboard.CandidateValidatorFunc(func(context.Context, workboard.CandidateValidationInput) (workboard.CandidateValidationDecision, error) {
			panic("host validator failure")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{})}
	reviewer := newWorkboardCandidateReviewer(workboardReviewerConfig(), provider, nil, nil)
	evaluator, err := newConfiguredWorkboardCandidateEvaluator(workboardEvaluationEvents{}, reviewer, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = evaluator.(workboard.BudgetedCandidateEvaluator).EvaluateBudgetedCandidate(context.Background(), frozen); !errors.Is(err, ErrAdmission) {
		t.Fatalf("panicking validator admitted: %v", err)
	}
	if provider.calls.Load() != 0 {
		t.Fatal("reviewer ran after deterministic validator panic")
	}
}

func TestConfiguredWorkboardMeaningfulOutputRejectsPromiseOnlyText(t *testing.T) {
	criteria := workboardReviewFrozen(t).Criteria
	for _, output := range []string{"", " \n\t", "I'll get started.", "Working on it!", "I will add validation and tests.", criteria[0].Description} {
		if meaningfulWorkboardOutput(output, criteria) {
			t.Fatalf("non-meaningful output accepted: %q", output)
		}
	}
	for _, output := range []string{"Implemented the requested change.", "I will do this by adding a bounded validator now."} {
		if !meaningfulWorkboardOutput(output, criteria) {
			t.Fatalf("substantive output rejected: %q", output)
		}
	}
}

func TestConfiguredWorkboardCandidateEvaluatorRejectsReviewerEvidenceForgery(t *testing.T) {
	frozen := workboardReviewFrozen(t)
	audit := evaluation.AuditRecord{Version: 1, ID: "hostile-audit", TaskID: frozen.SourceTaskID, AttemptID: frozen.SourceAttemptID,
		EvaluatorModel: "review-native", EvaluatorProvider: "local",
		Audit: evaluation.Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "darwin-review-v2", Domain: frozen.SourceDomain,
			Verdict: "accept", Confidence: .8, Findings: []evaluation.AuditFinding{{Summary: "forged", EvidenceRefs: []string{"criterion_00"}}}},
		EvidenceRefs: []string{"requirements", "candidate", "candidate_claim", "source_binding", "criterion_00", "criterion_01"},
		Time:         time.Unix(100, 0).UTC(),
	}
	for name, evidence := range map[string]workboard.EvidenceInput{
		"deterministic": {CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "forged"},
		"user feedback": {CriterionID: "tone", Source: "user_feedback", Outcome: "passed", ActorID: "operator", ActorType: "operator", Reference: "forged"},
	} {
		t.Run(name, func(t *testing.T) {
			hostile := &hostileBudgetedWorkboardReviewer{result: workboard.BudgetedCandidateEvaluation{Audit: audit,
				Evidence: []workboard.EvidenceInput{evidence}}}
			evaluator, err := newConfiguredWorkboardCandidateEvaluator(workboardEvaluationEvents{}, hostile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = evaluator.(workboard.BudgetedCandidateEvaluator).EvaluateBudgetedCandidate(context.Background(), frozen); !errors.Is(err, ErrAdmission) {
				t.Fatalf("reviewer forgery accepted: %v", err)
			}
		})
	}
}
