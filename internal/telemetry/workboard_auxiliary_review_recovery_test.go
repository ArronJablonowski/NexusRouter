package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestRecoverExpiredAuxiliaryReviewAtExactDeadlineIsConservativeAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	if recovered, err := store.RecoverExpiredAuxiliaryReview(ctx, "missing-auxiliary-review", func() time.Time {
		panic("missing admission must not sample recovery time")
	}); err != nil || recovered {
		t.Fatalf("missing admission recovered=%v err=%v", recovered, err)
	}
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "expired-recovery")
	admittedAt := time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	admission, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-expired-recovery", func() time.Time {
		return admittedAt
	})
	if err != nil || !created {
		t.Fatalf("admission=%+v created=%v err=%v", admission, created, err)
	}
	deadline := admittedAt.Add(time.Duration(admission.TimeLimitMS) * time.Millisecond)
	if recovered, recoverErr := store.RecoverExpiredAuxiliaryReview(ctx, admission.OperationID, func() time.Time {
		return deadline.Add(-time.Nanosecond)
	}); recoverErr != nil || recovered {
		t.Fatalf("pre-deadline recovered=%v err=%v", recovered, recoverErr)
	}
	if _, found, replayErr := store.ReplayAuxiliaryReviewSettlement(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed); replayErr != nil || found {
		t.Fatalf("pre-deadline settlement found=%v err=%v", found, replayErr)
	}
	if recovered, recoverErr := store.RecoverExpiredAuxiliaryReview(ctx, admission.OperationID, func() time.Time {
		return deadline
	}); recoverErr != nil || !recovered {
		t.Fatalf("deadline recovered=%v err=%v", recovered, recoverErr)
	}
	settlement, found, err := store.ReplayAuxiliaryReviewSettlement(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed)
	if err != nil || !found || settlement.Validate() != nil || !settlement.SettledAt.Equal(deadline) ||
		settlement.ChargedTimeMS != admission.TimeLimitMS || settlement.ChargedTokens != admission.TokenLimit ||
		settlement.ChargedCostMicros != admission.CostMicros || settlement.TimeChargeMode != workboard.AuxiliaryReviewConservative ||
		settlement.TokenChargeMode != workboard.AuxiliaryReviewConservative || settlement.CostChargeMode != workboard.AuxiliaryReviewConservative {
		t.Fatalf("settlement=%+v found=%v err=%v", settlement, found, err)
	}
	if recovered, recoverErr := store.RecoverExpiredAuxiliaryReview(ctx, admission.OperationID, func() time.Time {
		panic("terminal replay must not resample recovery time")
	}); recoverErr != nil || recovered {
		t.Fatalf("terminal replay recovered=%v err=%v", recovered, recoverErr)
	}
	replayed, found, err := store.ReplayAuxiliaryReviewSettlement(ctx, admission.OperationID, workboard.AuxiliaryReviewFailed)
	if err != nil || !found || replayed != settlement {
		t.Fatalf("replayed=%+v found=%v err=%v", replayed, found, err)
	}
	var admissions, settlements int
	if err = store.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM workboard_auxiliary_review_admissions WHERE operation_id=?),
		(SELECT count(*) FROM workboard_auxiliary_review_settlements WHERE operation_id=?)`,
		admission.OperationID, admission.OperationID).Scan(&admissions, &settlements); err != nil || admissions != 1 || settlements != 1 {
		t.Fatalf("admissions=%d settlements=%d err=%v", admissions, settlements, err)
	}
	release()
	<-submitted
}

func TestRecoverExpiredAuxiliaryReviewReplaysValidCompletedOutcome(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	evaluator := &budgetedReviewFixture{}
	service, request := prepareBudgetedReviewService(t, store, evaluator, &clock, "completed-recovery")
	if _, err := service.SubmitCandidate(ctx, request); err != nil {
		t.Fatal(err)
	}
	var candidateID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		request.BoardID, request.CardID, request.AttemptID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverExpiredAuxiliaryReview(ctx, "candidate-review-"+candidateID, func() time.Time {
		panic("completed outcome replay must not sample recovery time")
	}); err != nil || recovered {
		t.Fatalf("completed outcome recovered=%v err=%v", recovered, err)
	}
	if evaluator.calls.Load() != 1 {
		t.Fatalf("reviewer calls=%d", evaluator.calls.Load())
	}
}

func TestRecoverExpiredAuxiliaryReviewRejectsCorruptFenceAndTornCandidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(*testing.T, context.Context, *Store, workboard.CandidateEvaluationRequest, workboard.AuxiliaryReviewAdmissionRecord)
	}{
		{name: "fence", tamper: func(t *testing.T, ctx context.Context, store *Store, _ workboard.CandidateEvaluationRequest, admission workboard.AuxiliaryReviewAdmissionRecord) {
			if _, err := store.db.ExecContext(ctx, `DROP TRIGGER workboard_auxiliary_review_successor_fence_immutable_update;
				UPDATE workboard_auxiliary_review_successor_fences SET body=json_set(body,'$.claim_revision',claim_revision+1) WHERE admission_id=?`, admission.AdmissionID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "candidate", tamper: func(t *testing.T, ctx context.Context, store *Store, frozen workboard.CandidateEvaluationRequest, _ workboard.AuxiliaryReviewAdmissionRecord) {
			candidate := workboard.CandidateRecord{Version: 1, ID: frozen.CandidateID, BoardID: frozen.BoardID, CardID: frozen.CardID,
				AttemptID: frozen.AttemptID, Revision: 1, Digest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest,
				PolicyDigest: frozen.PolicyDigest, EvidenceDigest: workboard.EvidenceSetDigest([]workboard.EvidenceRecord{}),
				EvidenceCount: 0, Summary: frozen.Summary, ArtifactRefs: []string{}, SubmittedBy: frozen.WorkerID,
				CreatedAt: time.Date(2026, 9, 12, 15, 0, 5, 0, time.UTC)}
			body, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.ExecContext(ctx, `INSERT INTO workboard_candidates(id,board_id,card_id,attempt_id,revision,digest,criteria_digest,policy_digest,evidence_digest,evidence_count,submitted_by,created_at,body)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, candidate.ID, candidate.BoardID, candidate.CardID, candidate.AttemptID, candidate.Revision,
				candidate.Digest, candidate.CriteriaDigest, candidate.PolicyDigest, candidate.EvidenceDigest, candidate.EvidenceCount,
				candidate.SubmittedBy, candidate.CreatedAt.UnixNano(), body); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openExecutionAdmissionStore(t, ctx)
			defer store.Close()
			frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "recovery-torn-"+test.name)
			admittedAt := time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
			admission, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-recovery-torn-"+test.name, func() time.Time { return admittedAt })
			if err != nil || !created {
				t.Fatal(admission, created, err)
			}
			test.tamper(t, ctx, store, frozen, admission)
			deadline := admission.AdmittedAt.Add(time.Duration(admission.TimeLimitMS) * time.Millisecond)
			if recovered, recoverErr := store.RecoverExpiredAuxiliaryReview(ctx, admission.OperationID, func() time.Time { return deadline }); recovered || !errors.Is(recoverErr, ErrWorkboardCorrupt) {
				t.Fatalf("recovered=%v err=%v", recovered, recoverErr)
			}
			var settlements int
			if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM workboard_auxiliary_review_settlements WHERE admission_id=?`, admission.AdmissionID).Scan(&settlements); err != nil || settlements != 0 {
				t.Fatalf("settlements=%d err=%v", settlements, err)
			}
			release()
			<-submitted
		})
	}
}
