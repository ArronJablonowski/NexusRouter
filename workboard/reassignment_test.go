package workboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReassignmentRecordValidation(t *testing.T) {
	now := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
	valid := ReassignmentRecord{Version: 1, RecoveryID: "recovery", BoardID: "board", CardID: "card",
		PredecessorAttemptID: "attempt-one", PredecessorClaimID: "claim-one",
		SuccessorAttemptID: "attempt-two", SuccessorClaimID: "claim-two", CreatedAt: now}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ReassignmentRecord){
		"version":           func(r *ReassignmentRecord) { r.Version = 2 },
		"missing recovery":  func(r *ReassignmentRecord) { r.RecoveryID = "" },
		"same attempt":      func(r *ReassignmentRecord) { r.SuccessorAttemptID = r.PredecessorAttemptID },
		"same claim":        func(r *ReassignmentRecord) { r.SuccessorClaimID = r.PredecessorClaimID },
		"non UTC timestamp": func(r *ReassignmentRecord) { r.CreatedAt = r.CreatedAt.In(time.FixedZone("offset", 60)) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			change(&candidate)
			if candidate.Validate() == nil {
				t.Fatal("invalid reassignment accepted")
			}
		})
	}
}

func TestAttemptLineageRequiresExactSuccessorIdentity(t *testing.T) {
	now := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
	criteria := []AcceptanceCriterion{{Version: 1, ID: "criterion", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "validator", Description: "Pass.", Required: true}}
	body, _ := json.Marshal(criteria)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	lineage := &ReassignmentRecord{Version: 1, RecoveryID: "recovery", BoardID: "board", CardID: "card",
		PredecessorAttemptID: "attempt-one", PredecessorClaimID: "claim-one",
		SuccessorAttemptID: "attempt-two", SuccessorClaimID: "claim-two", CreatedAt: now}
	claim := &ClaimSnapshot{ID: "claim-two", BoardID: "board", CardID: "card", AttemptID: "attempt-two", Revision: 1,
		State: "active", OwnerID: "worker", OwnerType: "worker", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute)}
	attempt := AttemptSnapshot{ID: "attempt-two", BoardID: "board", CardID: "card", Ordinal: 2, Revision: 1, State: "running",
		WorkerID: "worker", CriteriaRevision: 1, CriteriaDigest: digest, PolicyDigest: strings.Repeat("a", 64),
		Budget: WorkBudget{AttemptLimit: 2}, Criteria: criteria, TaskIDs: []string{}, SessionIDs: []string{}, Claim: claim,
		Reassignment: lineage, Evidence: []EvidenceRecord{}, StartedAt: now}
	if err := attempt.Validate(); err != nil {
		t.Fatal(err)
	}
	history := AttemptHistoryRecord{Version: 1, ID: attempt.ID, BoardID: attempt.BoardID, CardID: attempt.CardID, Ordinal: attempt.Ordinal,
		Revision: 1, State: "running", WorkerID: attempt.WorkerID, CriteriaRevision: 1, Reassignment: lineage, StartedAt: now}
	if err := history.Validate(); err != nil {
		t.Fatal(err)
	}
	wrong := *lineage
	wrong.SuccessorClaimID = "other-claim"
	attempt.Reassignment = &wrong
	if attempt.Validate() == nil {
		t.Fatal("attempt accepted lineage for another successor claim")
	}
	attempt.Reassignment = lineage
	attempt.Ordinal = 1
	if attempt.Validate() == nil {
		t.Fatal("first attempt accepted reassignment lineage")
	}
	history.Reassignment = &wrong
	wrong.SuccessorAttemptID = "other-attempt"
	if history.Validate() == nil {
		t.Fatal("history accepted lineage for another successor attempt")
	}
}
