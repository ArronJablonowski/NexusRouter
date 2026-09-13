package workboard

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

type nilCandidateValidator struct{}

func (*nilCandidateValidator) ValidateCandidate(context.Context, CandidateValidationInput) (CandidateValidationDecision, error) {
	panic("typed nil must never be invoked")
}

func candidateValidatorFixture(t *testing.T, validatorID, output string) CandidateValidationInput {
	t.Helper()
	summary := output
	if strings.TrimSpace(summary) == "" {
		summary = "submitted candidate"
	}
	criterion := AcceptanceCriterion{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: validatorID, Description: "Tests pass.", Required: true}
	request := CandidateEvaluationRequest{Version: 1, BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim",
		CandidateID: "candidate", BindingKind: "runtime_budgeted", SourceTaskID: "task", SourceSessionID: "session",
		SourceTurnID: "turn", SourceAttemptID: "source-attempt", SourceCompletionEventID: "turn-completed",
		SourceCompletionSequence: 3, SourceCompletionDigest: strings.Repeat("b", 64), SourceOutput: output, SourceOutputDigest: SourceOutputDigest(output),
		SourceTerminalEventID: "task-completed", SourceTerminalSequence: 4, SourceTerminalDigest: strings.Repeat("d", 64),
		SourceDomain: "code", SourceProfile: "default", SourcePrivacy: "local_only", WorkerID: "worker",
		AdmissionID: "admission", AdmissionDigest: strings.Repeat("e", 64), SourceModelID: "source-model",
		SourceProviderID: "source-provider", ConfigID: strings.Repeat("f", 64), SourceTimeLimitMS: 1_000,
		SourceTokenLimit: 100, SourceCostMicros: 10, ExpectedCardRevision: 2, ExpectedClaimRevision: 1, CriteriaRevision: 1,
		CandidateDigest: CandidateContentDigest(summary, []string{}), CriteriaDigest: AcceptanceCriteriaDigest([]AcceptanceCriterion{criterion}),
		PolicyDigest: strings.Repeat("a", 64), Summary: summary, ArtifactRefs: []string{}, Criteria: []AcceptanceCriterion{criterion}}
	if request.Validate() != nil {
		t.Fatal("invalid fixture")
	}
	return CandidateValidationInput{Candidate: request, Criterion: criterion}
}

func TestCandidateValidatorRegistryIsImmutableTypedNilSafeAndProtectsStockIdentity(t *testing.T) {
	var calls atomic.Int32
	validator := CandidateValidatorFunc(func(context.Context, CandidateValidationInput) (CandidateValidationDecision, error) {
		calls.Add(1)
		return CandidateValidationDecision{Passed: true, Reference: "proof"}, nil
	})
	input := map[string]CandidateValidator{"project-tests-v1": validator}
	registry, err := NewCandidateValidatorRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	delete(input, "project-tests-v1")
	input["replacement"] = validator
	if _, err = registry.Resolve("project-tests-v1"); err != nil || calls.Load() != 0 {
		t.Fatalf("registry did not retain immutable binding: %v", err)
	}
	if _, err = registry.Resolve("replacement"); !errors.Is(err, &Violation{Code: CodeMissingNode}) {
		t.Fatalf("caller mutation changed registry: %v", err)
	}
	var typedNil *nilCandidateValidator
	var nilFunc CandidateValidatorFunc
	for _, invalid := range []CandidateValidator{nil, typedNil, nilFunc} {
		if got, buildErr := NewCandidateValidatorRegistry(map[string]CandidateValidator{"project-tests-v1": invalid}); buildErr == nil || got != nil {
			t.Fatal("nil validator admitted")
		}
	}
	if got, buildErr := NewCandidateValidatorRegistry(map[string]CandidateValidator{MeaningfulTextCandidateValidatorID: validator}); buildErr == nil || got != nil {
		t.Fatal("stock validator identity was replaceable")
	}
	if stock, resolveErr := registry.Resolve(MeaningfulTextCandidateValidatorID); resolveErr != nil || stock == nil {
		t.Fatalf("stock validator missing: %v", resolveErr)
	}
}

func TestInvokeCandidateValidatorOwnsInputAndContainsPanicAndCancellation(t *testing.T) {
	input := candidateValidatorFixture(t, "project-tests-v1", "Implemented and tested the change.")
	validator := CandidateValidatorFunc(func(_ context.Context, owned CandidateValidationInput) (CandidateValidationDecision, error) {
		owned.Candidate.Criteria[0].Description = "mutated"
		owned.Candidate.ArtifactRefs = append(owned.Candidate.ArtifactRefs, "forged")
		return CandidateValidationDecision{Passed: true, Reference: "proof"}, nil
	})
	decision, err := InvokeCandidateValidator(context.Background(), validator, input)
	if err != nil || !decision.Passed || input.Candidate.Criteria[0].Description != "Tests pass." || len(input.Candidate.ArtifactRefs) != 0 {
		t.Fatalf("owned invocation failed: decision=%+v input=%+v err=%v", decision, input, err)
	}
	panicking := CandidateValidatorFunc(func(context.Context, CandidateValidationInput) (CandidateValidationDecision, error) { panic("boom") })
	if _, err = InvokeCandidateValidator(context.Background(), panicking, input); err == nil {
		t.Fatal("validator panic escaped")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = InvokeCandidateValidator(canceled, validator, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation admitted: %v", err)
	}
	during, cancelDuring := context.WithCancel(context.Background())
	canceling := CandidateValidatorFunc(func(context.Context, CandidateValidationInput) (CandidateValidationDecision, error) {
		cancelDuring()
		return CandidateValidationDecision{Passed: true, Reference: "late-proof"}, nil
	})
	if _, err = InvokeCandidateValidator(during, canceling, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("decision returned after cancellation was admitted: %v", err)
	}
}

func TestMeaningfulTextCandidateValidatorBindsFrozenCompletion(t *testing.T) {
	registry, err := NewCandidateValidatorRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := registry.Resolve(MeaningfulTextCandidateValidatorID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		output string
		passed bool
	}{{"", false}, {"Working on it!", false}, {"Tests pass.", false}, {"Implemented and tested the requested change.", true}} {
		input := candidateValidatorFixture(t, MeaningfulTextCandidateValidatorID, test.output)
		decision, invokeErr := InvokeCandidateValidator(context.Background(), validator, input)
		if invokeErr != nil || decision.Passed != test.passed || decision.Reference != input.Candidate.SourceCompletionEventID {
			t.Fatalf("output=%q decision=%+v err=%v", test.output, decision, invokeErr)
		}
	}
}
