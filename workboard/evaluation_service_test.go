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
