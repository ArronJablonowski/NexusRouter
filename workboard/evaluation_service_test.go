package workboard

import (
	"strings"
	"testing"
)

func TestCandidateEvaluationRequestBindsCriteriaContent(t *testing.T) {
	criteria := []AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass.", Required: true}}
	request := CandidateEvaluationRequest{Version: 1, BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim",
		CandidateID: "candidate", BindingKind: "legacy", WorkerID: "worker", ExpectedCardRevision: 2, ExpectedClaimRevision: 1,
		CriteriaRevision: 1, CandidateDigest: CandidateContentDigest("candidate output", []string{}),
		CriteriaDigest: AcceptanceCriteriaDigest(criteria), PolicyDigest: strings.Repeat("a", 64), Summary: "candidate output",
		ArtifactRefs: []string{}, Criteria: criteria}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	tampered := request
	tampered.Criteria = append([]AcceptanceCriterion{}, request.Criteria...)
	tampered.Criteria[0].Description = "Different valid criterion."
	if err := tampered.Validate(); err == nil {
		t.Fatal("criteria content changed without changing its digest")
	}
}

func TestCandidateEvaluationRequestRequiresCompleteRuntimeSourceBinding(t *testing.T) {
	criteria := []AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass.", Required: true}}
	sourceOutput := "exact runtime output"
	request := CandidateEvaluationRequest{Version: 1, BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim",
		CandidateID: "candidate", BindingKind: "runtime_budgeted", SourceTaskID: "task", SourceSessionID: "session",
		SourceTurnID: "turn", SourceAttemptID: "source-attempt", SourceCompletionEventID: "turn-completed",
		SourceCompletionSequence: 3, SourceCompletionDigest: strings.Repeat("b", 64), SourceOutput: sourceOutput, SourceOutputDigest: SourceOutputDigest(sourceOutput),
		SourceTerminalEventID: "task-completed", SourceTerminalSequence: 4, SourceTerminalDigest: strings.Repeat("d", 64),
		SourceDomain: "code", SourceProfile: "default", SourcePrivacy: "local_only", WorkerID: "worker",
		AdmissionID: "admission", AdmissionDigest: strings.Repeat("e", 64), SourceModelID: "source-model",
		SourceProviderID: "source-provider", ConfigID: strings.Repeat("f", 64), SourceTimeLimitMS: 1_000,
		SourceTokenLimit: 100, SourceCostMicros: 10,
		ExpectedCardRevision: 2, ExpectedClaimRevision: 1, CriteriaRevision: 1,
		CandidateDigest: CandidateContentDigest("candidate output", []string{}), CriteriaDigest: AcceptanceCriteriaDigest(criteria),
		PolicyDigest: strings.Repeat("a", 64), Summary: "candidate output", ArtifactRefs: []string{}, Criteria: criteria}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CandidateEvaluationRequest){
		"turn":        func(r *CandidateEvaluationRequest) { r.SourceTurnID = "" },
		"attempt":     func(r *CandidateEvaluationRequest) { r.SourceAttemptID = "" },
		"event":       func(r *CandidateEvaluationRequest) { r.SourceCompletionEventID = "" },
		"sequence":    func(r *CandidateEvaluationRequest) { r.SourceCompletionSequence = 0 },
		"digest":      func(r *CandidateEvaluationRequest) { r.SourceCompletionDigest = "" },
		"output":      func(r *CandidateEvaluationRequest) { r.SourceOutputDigest = "" },
		"output body": func(r *CandidateEvaluationRequest) { r.SourceOutput = "different output" },
		"terminal":    func(r *CandidateEvaluationRequest) { r.SourceTerminalEventID = "" },
		"legacy leak": func(r *CandidateEvaluationRequest) {
			r.BindingKind, r.SourceTaskID, r.SourceSessionID = "legacy", "", ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("incomplete runtime source binding accepted")
			}
		})
	}
}
