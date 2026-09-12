package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardAuxiliaryReviewSchema42MigrationIsEmptyAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE workboard_auxiliary_review_settlements;
		DROP TABLE workboard_auxiliary_review_admissions; PRAGMA user_version=42`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, admissions, settlements int
	if err = store.db.QueryRow(`SELECT
		(SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM workboard_auxiliary_review_admissions),
		(SELECT count(*) FROM workboard_auxiliary_review_settlements)`).Scan(&version, &admissions, &settlements); err != nil ||
		version != 43 || admissions != 0 || settlements != 0 {
		t.Fatalf("schema=%d admissions=%d settlements=%d err=%v", version, admissions, settlements, err)
	}
}

func TestWorkboardAuxiliaryReviewMigrationRejectsRetainedFutureTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`PRAGMA user_version=42`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("schema-42 database retained schema-43 review tables")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 42 {
		t.Fatalf("failed migration changed schema=%d err=%v", version, err)
	}
}

func auxiliaryReviewAdmission(t *testing.T) workboard.AuxiliaryReviewAdmissionRecord {
	t.Helper()
	digest := strings.Repeat("a", 64)
	reservation := workboard.AuxiliaryReviewReservation{Version: 1, BoardID: "board", CardID: "card", AttemptID: "attempt",
		ClaimID: "claim", CandidateID: "candidate", CandidateDigest: digest, CriteriaDigest: digest, PolicyDigest: digest,
		ReviewerID: "reviewer", ModelID: "review-model", ProviderID: "provider", ConfigID: digest,
		TimeLimitMS: 1000, TokenLimit: 100, CostMicros: 50}
	reservationDigest, err := reservation.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	admission := workboard.AuxiliaryReviewAdmissionRecord{Version: 1, AdmissionID: "review-admission", OperationID: "review-operation",
		BoardID: reservation.BoardID, CardID: reservation.CardID, AttemptID: reservation.AttemptID, ClaimID: reservation.ClaimID,
		CandidateID: reservation.CandidateID, CandidateDigest: reservation.CandidateDigest, CriteriaDigest: reservation.CriteriaDigest,
		PolicyDigest: reservation.PolicyDigest, ReviewerID: reservation.ReviewerID, ModelID: reservation.ModelID,
		ProviderID: reservation.ProviderID, ConfigID: reservation.ConfigID, TimeLimitMS: reservation.TimeLimitMS,
		TokenLimit: reservation.TokenLimit, CostMicros: reservation.CostMicros, AdmittedAt: time.Unix(0, 2).UTC(),
		ReservationDigest: reservationDigest}
	admission.AdmissionDigest, err = admission.CanonicalDigest()
	if err != nil || admission.Validate() != nil {
		t.Fatal("invalid fixture", err)
	}
	return admission
}

func insertAuxiliaryReviewAdmission(t *testing.T, db *sql.DB, admission workboard.AuxiliaryReviewAdmissionRecord) error {
	t.Helper()
	body, err := json.Marshal(admission)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO workboard_auxiliary_review_admissions(admission_id,operation_id,board_id,card_id,attempt_id,claim_id,
		candidate_id,candidate_digest,criteria_digest,policy_digest,reviewer_id,model_id,provider_id,config_id,time_limit_ms,
		token_limit,cost_micros,admitted_at,reservation_digest,admission_digest,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		admission.AdmissionID, admission.OperationID, admission.BoardID, admission.CardID, admission.AttemptID, admission.ClaimID,
		admission.CandidateID, admission.CandidateDigest, admission.CriteriaDigest, admission.PolicyDigest, admission.ReviewerID,
		admission.ModelID, admission.ProviderID, admission.ConfigID, admission.TimeLimitMS, admission.TokenLimit,
		admission.CostMicros, admission.AdmittedAt.UnixNano(), admission.ReservationDigest, admission.AdmissionDigest, body)
	return err
}

func auxiliaryReviewSettlement(t *testing.T, admission workboard.AuxiliaryReviewAdmissionRecord, disposition workboard.AuxiliaryReviewDisposition) workboard.AuxiliaryReviewSettlementRecord {
	t.Helper()
	settlement := workboard.AuxiliaryReviewSettlementRecord{Version: 1, SettlementID: "review-settlement", AdmissionID: admission.AdmissionID,
		AdmissionDigest: admission.AdmissionDigest, ReservationDigest: admission.ReservationDigest, OperationID: admission.OperationID,
		BoardID: admission.BoardID, CardID: admission.CardID, AttemptID: admission.AttemptID, ClaimID: admission.ClaimID,
		CandidateID: admission.CandidateID, CandidateDigest: admission.CandidateDigest, CriteriaDigest: admission.CriteriaDigest,
		PolicyDigest: admission.PolicyDigest, ReviewerID: admission.ReviewerID, ModelID: admission.ModelID,
		ProviderID: admission.ProviderID, ConfigID: admission.ConfigID, TimeLimitMS: admission.TimeLimitMS,
		TokenLimit: admission.TokenLimit, CostMicros: admission.CostMicros, AdmittedAt: admission.AdmittedAt,
		Disposition: disposition, ChargedTimeMS: admission.TimeLimitMS, ChargedTokens: admission.TokenLimit,
		ChargedCostMicros: admission.CostMicros, TimeChargeMode: workboard.AuxiliaryReviewConservative,
		TokenChargeMode: workboard.AuxiliaryReviewConservative, CostChargeMode: workboard.AuxiliaryReviewConservative,
		SettledAt: admission.AdmittedAt.Add(time.Second)}
	var err error
	settlement.SettlementDigest, err = settlement.CanonicalDigest()
	if err != nil || settlement.Validate() != nil {
		t.Fatal("invalid settlement fixture", err)
	}
	return settlement
}

func insertAuxiliaryReviewSettlement(t *testing.T, db *sql.DB, settlement workboard.AuxiliaryReviewSettlementRecord) error {
	t.Helper()
	body, err := json.Marshal(settlement)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO workboard_auxiliary_review_settlements(settlement_id,admission_id,admission_digest,reservation_digest,
		operation_id,board_id,card_id,attempt_id,claim_id,candidate_id,candidate_digest,criteria_digest,policy_digest,reviewer_id,
		model_id,provider_id,config_id,time_limit_ms,token_limit,cost_micros,admitted_at,disposition,charged_time_ms,charged_tokens,
		charged_cost_micros,time_charge_mode,token_charge_mode,cost_charge_mode,settled_at,settlement_digest,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, settlement.SettlementID, settlement.AdmissionID,
		settlement.AdmissionDigest, settlement.ReservationDigest, settlement.OperationID, settlement.BoardID, settlement.CardID,
		settlement.AttemptID, settlement.ClaimID, settlement.CandidateID, settlement.CandidateDigest, settlement.CriteriaDigest,
		settlement.PolicyDigest, settlement.ReviewerID, settlement.ModelID, settlement.ProviderID, settlement.ConfigID,
		settlement.TimeLimitMS, settlement.TokenLimit, settlement.CostMicros, settlement.AdmittedAt.UnixNano(), settlement.Disposition,
		settlement.ChargedTimeMS, settlement.ChargedTokens, settlement.ChargedCostMicros, settlement.TimeChargeMode,
		settlement.TokenChargeMode, settlement.CostChargeMode, settlement.SettledAt.UnixNano(), settlement.SettlementDigest, body)
	return err
}

func TestWorkboardAuxiliaryReviewBindingsUniquenessAndImmutability(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('task','session',1,'running')`); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	admission := auxiliaryReviewAdmission(t)
	bad := admission
	bad.CriteriaDigest = strings.Repeat("b", 64)
	if err = insertAuxiliaryReviewAdmission(t, store.db, bad); err == nil || !strings.Contains(err.Error(), "admission binding mismatch") {
		t.Fatalf("criteria drift admitted: %v", err)
	}
	if err = insertAuxiliaryReviewAdmission(t, store.db, admission); err != nil {
		t.Fatal(err)
	}
	duplicate := admission
	duplicate.AdmissionID, duplicate.OperationID = "other-admission", "other-operation"
	duplicate.AdmissionDigest, _ = duplicate.CanonicalDigest()
	if err = insertAuxiliaryReviewAdmission(t, store.db, duplicate); err == nil {
		t.Fatal("second review admitted for one claim attempt")
	}
	if _, err = store.db.Exec(`UPDATE workboard_auxiliary_review_admissions SET model_id='forged' WHERE admission_id=?`, admission.AdmissionID); err == nil {
		t.Fatal("admission update accepted")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_auxiliary_review_admissions WHERE admission_id=?`, admission.AdmissionID); err == nil {
		t.Fatal("admission delete accepted")
	}
	completed := auxiliaryReviewSettlement(t, admission, workboard.AuxiliaryReviewCompleted)
	if err = insertAuxiliaryReviewSettlement(t, store.db, completed); err == nil || !strings.Contains(err.Error(), "settlement binding mismatch") {
		t.Fatalf("completed settlement without candidate: %v", err)
	}
	failed := auxiliaryReviewSettlement(t, admission, workboard.AuxiliaryReviewFailed)
	if err = insertAuxiliaryReviewSettlement(t, store.db, failed); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_auxiliary_review_settlements SET disposition='canceled' WHERE settlement_id=?`, failed.SettlementID); err == nil {
		t.Fatal("settlement update accepted")
	}
	if _, err = store.db.Exec(`DELETE FROM workboard_auxiliary_review_settlements WHERE settlement_id=?`, failed.SettlementID); err == nil {
		t.Fatal("settlement delete accepted")
	}
}

func TestWorkboardAuxiliaryReviewSchemaTamperFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_settlement_binding;
		CREATE TRIGGER workboard_auxiliary_review_settlement_binding BEFORE INSERT ON workboard_auxiliary_review_settlements
		BEGIN SELECT RAISE(ABORT,'forged'); END`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("forged schema-43 trigger accepted")
	}
}
