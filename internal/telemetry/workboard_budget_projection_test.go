package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardBudgetProjectionRejectsAuxiliaryAccountingGuardTamper(t *testing.T) {
	for name := range auxiliaryReviewAccountingGuardSQL() {
		name := name
		t.Run(name+"/missing", func(t *testing.T) {
			store, boardID, cardID := budgetProjectionCard(t)
			if _, err := store.db.Exec("DROP TRIGGER " + name); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadWorkboardBudgetProjection(context.Background(), boardID, cardID); !errors.Is(err, ErrWorkboardCorrupt) {
				t.Fatalf("missing guard accepted: %v", err)
			}
		})
		t.Run(name+"/altered", func(t *testing.T) {
			store, boardID, cardID := budgetProjectionCard(t)
			if _, err := store.db.Exec("DROP TRIGGER " + name + "; CREATE TRIGGER " + name +
				" BEFORE INSERT ON workboard_auxiliary_review_admissions BEGIN SELECT RAISE(ABORT,'forged'); END"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadWorkboardBudgetProjection(context.Background(), boardID, cardID); !errors.Is(err, ErrWorkboardCorrupt) {
				t.Fatalf("altered guard accepted: %v", err)
			}
		})
	}
}

func TestWorkboardBudgetProjectionRejectsOrphanAuxiliarySettlement(t *testing.T) {
	store, boardID, cardID := budgetProjectionCard(t)
	store.db.SetMaxOpenConns(1)
	if _, err := store.db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER workboard_auxiliary_review_admission_binding`); err != nil {
		t.Fatal(err)
	}
	actual := reboundAuxiliaryReviewAdmission(t, auxiliaryReviewAdmission(t), "other-board", "other-card", "other-attempt", "other-claim")
	if err := insertAuxiliaryReviewAdmission(t, store.db, actual); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(auxiliaryReviewAccountingGuardSQL()["workboard_auxiliary_review_admission_binding"]); err != nil {
		t.Fatal(err)
	}
	target := reboundAuxiliaryReviewAdmission(t, actual, boardID, cardID, "target-attempt", "target-claim")
	settlement := auxiliaryReviewSettlement(t, target, workboard.AuxiliaryReviewFailed)
	if _, err := store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_settlement_binding`); err != nil {
		t.Fatal(err)
	}
	if err := insertAuxiliaryReviewSettlement(t, store.db, settlement); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(auxiliaryReviewAccountingGuardSQL()["workboard_auxiliary_review_settlement_binding"]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadWorkboardBudgetProjection(context.Background(), boardID, cardID); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("orphan settlement accepted: %v", err)
	}
}

func TestWorkboardBudgetProjectionRejectsAuxiliarySettlementAdmissionDrift(t *testing.T) {
	store, boardID, cardID := budgetProjectionCard(t)
	store.db.SetMaxOpenConns(1)
	if _, err := store.db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER workboard_auxiliary_review_admission_binding`); err != nil {
		t.Fatal(err)
	}
	admission := reboundAuxiliaryReviewAdmission(t, auxiliaryReviewAdmission(t), boardID, cardID, "attempt", "claim")
	if err := insertAuxiliaryReviewAdmission(t, store.db, admission); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(auxiliaryReviewAccountingGuardSQL()["workboard_auxiliary_review_admission_binding"]); err != nil {
		t.Fatal(err)
	}
	drifted := admission
	drifted.ConfigID = strings.Repeat("b", 64)
	drifted = reboundAuxiliaryReviewAdmission(t, drifted, boardID, cardID, admission.AttemptID, admission.ClaimID)
	settlement := auxiliaryReviewSettlement(t, drifted, workboard.AuxiliaryReviewFailed)
	if _, err := store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_settlement_binding`); err != nil {
		t.Fatal(err)
	}
	if err := insertAuxiliaryReviewSettlement(t, store.db, settlement); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(auxiliaryReviewAccountingGuardSQL()["workboard_auxiliary_review_settlement_binding"]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadWorkboardBudgetProjection(context.Background(), boardID, cardID); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("settlement/admission drift accepted: %v", err)
	}
}

func budgetProjectionCard(t *testing.T) (*Store, string, string) {
	t.Helper()
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	card := createCardForTest(t, ctx, service, boardID, "projection-card-key", "Projection card", 1, 1, nil)
	return store, boardID, card.ID
}

func reboundAuxiliaryReviewAdmission(t *testing.T, admission workboard.AuxiliaryReviewAdmissionRecord,
	boardID, cardID, attemptID, claimID string,
) workboard.AuxiliaryReviewAdmissionRecord {
	t.Helper()
	admission.BoardID, admission.CardID, admission.AttemptID, admission.ClaimID = boardID, cardID, attemptID, claimID
	reservation := workboard.AuxiliaryReviewReservation{Version: workboard.AuxiliaryReviewReservationVersion,
		BoardID: admission.BoardID, CardID: admission.CardID, AttemptID: admission.AttemptID, ClaimID: admission.ClaimID,
		CandidateID: admission.CandidateID, CandidateDigest: admission.CandidateDigest, CriteriaDigest: admission.CriteriaDigest,
		PolicyDigest: admission.PolicyDigest, ReviewerID: admission.ReviewerID, ModelID: admission.ModelID,
		ProviderID: admission.ProviderID, ConfigID: admission.ConfigID, TimeLimitMS: admission.TimeLimitMS,
		TokenLimit: admission.TokenLimit, CostMicros: admission.CostMicros}
	var err error
	admission.ReservationDigest, err = reservation.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	admission.AdmissionDigest, err = admission.CanonicalDigest()
	if err != nil || admission.Validate() != nil {
		t.Fatalf("invalid rebound admission: %+v err=%v", admission, err)
	}
	return admission
}
