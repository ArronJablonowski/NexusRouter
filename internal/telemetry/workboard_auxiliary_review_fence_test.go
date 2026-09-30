package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestAuxiliaryReviewAdmissionAtomicallyCreatesExactSuccessorFence(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "successor-fence")
	defer func() {
		release()
		<-submitted
	}()
	admittedAt := time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	admission, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-successor", func() time.Time { return admittedAt })
	if err != nil || !created {
		t.Fatalf("admission=%+v created=%v err=%v", admission, created, err)
	}
	fence, found, err := store.AuxiliaryReviewSuccessorFence(ctx, admission.AdmissionID)
	if err != nil || !found || fence.Validate() != nil {
		t.Fatalf("fence=%+v found=%v err=%v", fence, found, err)
	}
	if fence.AdmissionID != admission.AdmissionID || fence.AdmissionDigest != admission.AdmissionDigest ||
		fence.OperationID != admission.OperationID || fence.BoardID != frozen.BoardID || fence.CardID != frozen.CardID ||
		fence.AttemptID != frozen.AttemptID || fence.ClaimID != frozen.ClaimID ||
		fence.CardRevision != frozen.ExpectedCardRevision || fence.ClaimRevision != frozen.ExpectedClaimRevision ||
		fence.CriteriaRevision != frozen.CriteriaRevision || fence.CandidateID != frozen.CandidateID ||
		fence.CandidateDigest != frozen.CandidateDigest || fence.CriteriaDigest != frozen.CriteriaDigest ||
		fence.PolicyDigest != frozen.PolicyDigest || !fence.AdmittedAt.Equal(admittedAt) ||
		!fence.DeadlineAt.Equal(admittedAt.Add(time.Duration(reservation.TimeLimitMS)*time.Millisecond)) {
		t.Fatalf("successor fence lost exact admission snapshot: %+v", fence)
	}

	replayed, created, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, admission.OperationID, func() time.Time {
		panic("replay must not create or resample a successor fence")
	})
	if err != nil || created || replayed != admission {
		t.Fatalf("replay=%+v created=%v err=%v", replayed, created, err)
	}
	replayedFence, found, err := store.AuxiliaryReviewSuccessorFence(ctx, admission.AdmissionID)
	if err != nil || !found || replayedFence != fence {
		t.Fatalf("replayed fence=%+v found=%v err=%v", replayedFence, found, err)
	}
}

func TestAuxiliaryReviewFenceFailureRollsBackAdmission(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "fence-rollback")
	defer func() {
		release()
		<-submitted
	}()
	if _, err := store.db.Exec(`CREATE TRIGGER reject_successor_fence BEFORE INSERT ON workboard_auxiliary_review_successor_fences
		BEGIN SELECT RAISE(ABORT,'injected successor fence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-rollback", func() time.Time {
		return time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	}); err == nil {
		t.Fatal("injected fence failure accepted")
	}
	var admissions, fences int
	if err := store.db.QueryRow(`SELECT
		(SELECT count(*) FROM workboard_auxiliary_review_admissions WHERE operation_id='auxiliary-operation-rollback'),
		(SELECT count(*) FROM workboard_auxiliary_review_successor_fences WHERE operation_id='auxiliary-operation-rollback')`).Scan(&admissions, &fences); err != nil || admissions != 0 || fences != 0 {
		t.Fatalf("partial admission survived: admissions=%d fences=%d err=%v", admissions, fences, err)
	}
}

func TestSchema43AdmissionDoesNotGainSuccessorAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	legacy := auxiliaryReviewAdmission(t)
	if err = insertAuxiliaryReviewAdmission(t, store.db, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_successor_fence_immutable_delete;
		DROP TRIGGER workboard_auxiliary_review_successor_fence_immutable_update;
		DROP TRIGGER workboard_auxiliary_review_successor_fence_binding;
		DROP TABLE workboard_auxiliary_review_successor_fences;
		DROP TRIGGER workboard_auxiliary_review_legacy_admission_sealed_insert;
		DROP TRIGGER workboard_auxiliary_review_legacy_admission_immutable_update;
		DROP TRIGGER workboard_auxiliary_review_legacy_admission_immutable_delete;
		DROP TABLE workboard_auxiliary_review_legacy_admissions;
		PRAGMA user_version=43`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if fence, found, err := reopened.AuxiliaryReviewSuccessorFence(ctx, legacy.AdmissionID); err != nil || found || fence != (workboard.AuxiliaryReviewSuccessorFence{}) {
		t.Fatalf("schema-43 admission gained authority: fence=%+v found=%v err=%v", fence, found, err)
	}
}

func TestSchema43DeclarationRejectsRetainedSuccessorAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "retained-fence")
	if _, _, err = store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-retained", func() time.Time {
		return time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	}); err != nil {
		t.Fatal(err)
	}
	release()
	if err = <-submitted; err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`PRAGMA user_version=43`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("schema-43 declaration retained schema-44 successor authority")
	}
}

func TestAuxiliaryReviewSuccessorFenceIsImmutableAndBodyValidated(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	frozen, reservation, submitted, release := prepareAuxiliaryReviewCandidate(t, ctx, store, "fence-integrity")
	defer func() {
		release()
		<-submitted
	}()
	admission, _, err := store.AdmitAuxiliaryReview(ctx, frozen, reservation, "auxiliary-operation-integrity", func() time.Time {
		return time.Date(2026, 9, 12, 15, 0, 4, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_auxiliary_review_successor_fences SET claim_revision=claim_revision+1 WHERE admission_id=?`, admission.AdmissionID); err == nil {
		t.Fatal("successor fence update accepted")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_auxiliary_review_successor_fences WHERE admission_id=?`, admission.AdmissionID); err == nil {
		t.Fatal("successor fence delete accepted")
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_successor_fence_immutable_update;
		UPDATE workboard_auxiliary_review_successor_fences SET body=json_set(body,'$.claim_revision',claim_revision+1) WHERE admission_id=?`, admission.AdmissionID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.AuxiliaryReviewSuccessorFence(ctx, admission.AdmissionID); !found || !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("noncanonical successor body accepted: found=%v err=%v", found, err)
	}
}
