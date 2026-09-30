package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardLifecycleReaderCrossChecksEvaluationProjections(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("reader-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "reader-claim-key-01", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(time.Second)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed",
		ActorID: "go-test", ActorType: "validator", Reference: "test-report"}}}
	workerEvaluation := newTestEvaluationService(t, store, worker, evaluator, &clock)
	request := workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "reader-candidate-001", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1,
		CriteriaRevision: card.CriteriaRevision, Summary: "Implementation passed", ArtifactRefs: []string{"artifact-one"}}
	if _, err = workerEvaluation.SubmitCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	operator := newTestEvaluationService(t, store, workboard.Actor{ID: "operator-a", Type: "operator"}, evaluator, &clock)
	decision := workboard.DecideCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		IdempotencyKey: "reader-accept-key-01", ExpectedCardRevision: current.Revision, CriteriaRevision: card.CriteriaRevision,
		CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceHeadRevision: int64(len(evidence)),
		EvidenceSetDigest: workboard.EvidenceSetDigest(evidence), PolicyDigest: candidate.PolicyDigest, Evidence: "objective evidence accepted"}
	if _, err = operator.AcceptCandidate(ctx, decision); err != nil {
		t.Fatal(err)
	}
	read := func() error {
		items, readErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{card.ID})
		if readErr != nil {
			return readErr
		}
		item := items[card.ID]
		if item.Attempt == nil || item.Attempt.Candidate == nil || item.Attempt.Acceptance == nil || len(item.Attempt.Evidence) != 1 {
			t.Fatalf("incomplete lifecycle: %+v", item)
		}
		if item.Attempt.Acceptance.PriorEvidenceHeadRevision != decision.EvidenceHeadRevision ||
			item.Attempt.Acceptance.PriorEvidenceSetDigest != decision.EvidenceSetDigest ||
			item.Attempt.Acceptance.Rationale != decision.Evidence {
			t.Fatalf("acceptance body-only fields lost: %+v", item.Attempt.Acceptance)
		}
		return nil
	}
	if err = read(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, update, restore string
	}{
		{"candidate", `UPDATE workboard_candidates SET digest=? WHERE id=?`, `UPDATE workboard_candidates SET digest=? WHERE id=?`},
		{"evidence", `UPDATE workboard_evidence SET reference=? WHERE id=?`, `UPDATE workboard_evidence SET reference=? WHERE id=?`},
		{"acceptance", `UPDATE workboard_acceptances SET decision=? WHERE attempt_id=?`, `UPDATE workboard_acceptances SET decision=? WHERE attempt_id=?`},
	} {
		var id, original, forged string
		switch test.name {
		case "candidate":
			id, original, forged = candidate.ID, candidate.Digest, candidate.PolicyDigest
		case "evidence":
			id, original, forged = evidence[0].ID, evidence[0].Reference, "forged-reference"
		case "acceptance":
			id, original, forged = attemptID, "accepted", "rejected"
		}
		if _, err = store.db.Exec(test.update, forged, id); err != nil {
			t.Fatal(test.name, err)
		}
		if err = read(); !errors.Is(err, ErrWorkboardCorrupt) {
			t.Fatalf("%s normalized tamper was accepted: %v", test.name, err)
		}
		if _, err = store.db.Exec(test.restore, original, id); err != nil {
			t.Fatal(test.name, err)
		}
	}
}
