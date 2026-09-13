package workboard

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCoordinateDeterministicEvidenceHonorsEvidenceAuthority(t *testing.T) {
	objective := AcceptanceCriterion{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass.", Required: true}
	subjective := AcceptanceCriterion{Version: 1, ID: "voice", Kind: "subjective", RequiredSource: "user_feedback",
		ValidatorID: "operator", Description: "The tone is appropriate.", Required: true}
	cases := []struct {
		name     string
		criteria []AcceptanceCriterion
		evidence []EvidenceRecord
		want     CoordinatedDecision
	}{
		{name: "objective pass", criteria: []AcceptanceCriterion{objective}, evidence: []EvidenceRecord{
			{CriterionID: "tests", Source: "model_audit", Outcome: "failed", ActorID: "critic", ActorType: "model", Reference: "hostile-audit"},
			{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "test-report"},
		}, want: DecisionAccepted},
		{name: "objective failure rejects despite subjective", criteria: []AcceptanceCriterion{objective, subjective}, evidence: []EvidenceRecord{
			{CriterionID: "tests", Source: "deterministic", Outcome: "failed", ActorID: "go-test", ActorType: "validator", Reference: "test-report"},
			{CriterionID: "voice", Source: "model_audit", Outcome: "passed", ActorID: "critic", ActorType: "model", Reference: "audit"},
		}, want: DecisionRejected},
		{name: "subjective waits for operator", criteria: []AcceptanceCriterion{objective, subjective}, evidence: []EvidenceRecord{
			{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "test-report"},
			{CriterionID: "voice", Source: "model_audit", Outcome: "passed", ActorID: "critic", ActorType: "model", Reference: "audit"},
		}, want: DecisionPending},
		{name: "missing objective waits", criteria: []AcceptanceCriterion{objective}, evidence: []EvidenceRecord{
			{CriterionID: "tests", Source: "model_audit", Outcome: "passed", ActorID: "critic", ActorType: "model", Reference: "audit"},
		}, want: DecisionPending},
		{name: "wrong validator cannot decide", criteria: []AcceptanceCriterion{objective}, evidence: []EvidenceRecord{
			{CriterionID: "tests", Source: "deterministic", Outcome: "failed", ActorID: "other-validator", ActorType: "validator", Reference: "forged"},
		}, want: DecisionPending},
		{name: "optional failure cannot reject", criteria: []AcceptanceCriterion{{Version: 1, ID: "lint", Kind: "objective", RequiredSource: "deterministic",
			ValidatorID: "lint", Description: "Lint passes.", Required: false}}, evidence: []EvidenceRecord{
			{CriterionID: "lint", Source: "deterministic", Outcome: "failed", ActorID: "lint", ActorType: "validator", Reference: "lint-report"},
		}, want: DecisionAccepted},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, _ := coordinateDeterministicEvidence(test.criteria, test.evidence)
			if got != test.want {
				t.Fatalf("decision=%q want=%q", got, test.want)
			}
		})
	}
}

func TestCoordinatedRationaleReferencesOnlyDecisionEvidence(t *testing.T) {
	got := coordinatedRationale(DecisionAccepted, []string{"validator-b", "validator-a"})
	if got != "criterion coordinator accepted; deterministic evidence references: validator-b,validator-a" {
		t.Fatal(got)
	}
}

type rotatingAcceptanceRepository struct {
	ids       []string
	snapshots map[string]AcceptanceDecisionSnapshot
}

func (r *rotatingAcceptanceRepository) ListAcceptanceCandidates(_ context.Context, _ string, after string, limit int) (AcceptanceCandidatePage, error) {
	start := sort.SearchStrings(r.ids, after)
	for start < len(r.ids) && r.ids[start] <= after {
		start++
	}
	end := min(start+limit, len(r.ids))
	ids := append([]string(nil), r.ids[start:end]...)
	page := AcceptanceCandidatePage{CardIDs: ids, HasMore: end < len(r.ids)}
	if len(ids) > 0 {
		page.NextCursor = ids[len(ids)-1]
	}
	return page, nil
}

func (r *rotatingAcceptanceRepository) ReadAcceptanceDecision(_ context.Context, _ string, cardID string) (AcceptanceDecisionSnapshot, error) {
	return r.snapshots[cardID], nil
}

type recordingCandidateDecisionWriter struct{ accepted []string }

func (w *recordingCandidateDecisionWriter) AcceptCandidate(_ context.Context, request DecideCandidateRequest) (OperationReceipt, error) {
	w.accepted = append(w.accepted, request.CardID)
	return OperationReceipt{}, nil
}

func (*recordingCandidateDecisionWriter) RejectCandidate(context.Context, DecideCandidateRequest) (OperationReceipt, error) {
	return OperationReceipt{}, nil
}

func TestAcceptanceReconciliationRotatesPastSubjectivePage(t *testing.T) {
	repository := &rotatingAcceptanceRepository{snapshots: map[string]AcceptanceDecisionSnapshot{}}
	for index := 0; index < MaxAcceptanceCandidatePageItems+1; index++ {
		cardID := fmt.Sprintf("card-%03d", index)
		repository.ids = append(repository.ids, cardID)
		repository.snapshots[cardID] = acceptanceReconciliationSnapshot(cardID, false)
	}
	objectiveID := fmt.Sprintf("card-%03d", MaxAcceptanceCandidatePageItems+1)
	repository.ids = append(repository.ids, objectiveID)
	repository.snapshots[objectiveID] = acceptanceReconciliationSnapshot(objectiveID, true)
	writer := &recordingCandidateDecisionWriter{}
	coordinator, err := NewAcceptanceCoordinator(repository, writer)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.ReconcileBoard(context.Background(), "board-a", MaxAcceptanceCandidatePageItems)
	if err != nil || first.Scanned != MaxAcceptanceCandidatePageItems || first.Pending != MaxAcceptanceCandidatePageItems || len(writer.accepted) != 0 {
		t.Fatalf("first=%+v accepted=%v err=%v", first, writer.accepted, err)
	}
	second, err := coordinator.ReconcileBoard(context.Background(), "board-a", MaxAcceptanceCandidatePageItems)
	if err != nil || second.Scanned == 0 || second.Accepted != 1 || len(writer.accepted) != 1 || writer.accepted[0] != objectiveID {
		t.Fatalf("second=%+v accepted=%v err=%v", second, writer.accepted, err)
	}
}

func acceptanceReconciliationSnapshot(cardID string, objective bool) AcceptanceDecisionSnapshot {
	now := time.Date(2026, 9, 13, 20, 0, 0, 0, time.UTC)
	criterion := AcceptanceCriterion{Version: 1, ID: "criterion", Kind: "subjective", RequiredSource: "user_feedback",
		ValidatorID: "operator", Description: "Operator approval.", Required: true}
	evidence := []EvidenceRecord{}
	if objective {
		criterion.Kind, criterion.RequiredSource, criterion.ValidatorID, criterion.Description = "objective", "deterministic", "validator", "Validation passes."
	}
	criteria := []AcceptanceCriterion{criterion}
	criteriaDigest, policyDigest, candidateDigest := AcceptanceCriteriaDigest(criteria), strings.Repeat("a", 64), strings.Repeat("c", 64)
	candidateID, attemptID, claimID := "candidate-"+cardID, "attempt-"+cardID, "claim-"+cardID
	if objective {
		evidence = append(evidence, EvidenceRecord{Version: 1, ID: "evidence-" + cardID, Revision: 1, BoardID: "board-a", CardID: cardID,
			AttemptID: attemptID, CandidateID: candidateID, CriterionID: criterion.ID, Source: "deterministic", Outcome: "passed",
			ActorID: criterion.ValidatorID, ActorType: "validator", Reference: "reference-" + cardID, CandidateDigest: candidateDigest,
			CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, CreatedAt: now})
	}
	released := now.Add(time.Second)
	return AcceptanceDecisionSnapshot{CardRevision: 3, Attempt: AttemptSnapshot{ID: attemptID, BoardID: "board-a", CardID: cardID,
		Ordinal: 1, Revision: 2, State: "review", WorkerID: "worker", CriteriaRevision: 1, CriteriaDigest: criteriaDigest,
		PolicyDigest: policyDigest, Budget: WorkBudget{AttemptLimit: 1}, Criteria: criteria, TaskIDs: []string{}, SessionIDs: []string{},
		Claim: &ClaimSnapshot{ID: claimID, BoardID: "board-a", CardID: cardID, AttemptID: attemptID, Revision: 2, State: string(LeaseReleased),
			OwnerID: "worker", OwnerType: "worker", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute), ReleasedAt: &released},
		Candidate: &CandidateRecord{Version: 1, ID: candidateID, BoardID: "board-a", CardID: cardID, AttemptID: attemptID, Revision: 1,
			Digest: candidateDigest, CriteriaDigest: criteriaDigest, PolicyDigest: policyDigest, EvidenceDigest: EvidenceSetDigest(evidence),
			EvidenceCount: len(evidence), Summary: "candidate", ArtifactRefs: []string{}, SubmittedBy: "worker", CreatedAt: now},
		Evidence: evidence, StartedAt: now, EndedAt: &released}}
}
