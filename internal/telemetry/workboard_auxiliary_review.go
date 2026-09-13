package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// AuxiliaryReviewMeasurements remains an alias for compatibility with storage
// callers. The provider-neutral contract is owned by workboard.
type AuxiliaryReviewMeasurements = workboard.AuxiliaryReviewMeasurements

// AdmitAuxiliaryReview atomically reserves card-owned capacity for one frozen
// candidate review. Exact operation replays return the original admission;
// unresolved admissions remain fully chargeable after restart.
func (s *Store) AdmitAuxiliaryReview(ctx context.Context, frozen workboard.CandidateEvaluationRequest,
	reservation workboard.AuxiliaryReviewReservation, operationID string, now func() time.Time,
) (workboard.AuxiliaryReviewAdmissionRecord, bool, error) {
	if ctx == nil || s == nil || s.db == nil || frozen.Validate() != nil || reservation.Validate() != nil ||
		!validWorkboardID(operationID) || now == nil || !auxiliaryReviewMatchesFrozen(reservation, frozen) ||
		(frozen.BindingKind == "runtime_budgeted" && reservation.ModelID == frozen.SourceModelID && reservation.ProviderID == frozen.SourceProviderID) {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, invalidWorkboard("auxiliary_review_admission")
	}
	reservationDigest, err := reservation.CanonicalDigest()
	if err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	if prior, found, readErr := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operationID); found || readErr != nil {
		if readErr != nil {
			return workboard.AuxiliaryReviewAdmissionRecord{}, false, readErr
		}
		want, buildErr := auxiliaryReviewAdmissionRecord(reservation, operationID, prior.AdmissionID, prior.AdmittedAt, reservationDigest)
		if buildErr != nil || prior != want {
			return workboard.AuxiliaryReviewAdmissionRecord{}, false, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
		}
		return prior, false, nil
	}
	if _, found, readErr := readAuxiliaryReviewAdmissionForCandidate(ctx, tx, frozen.BoardID, frozen.CardID, frozen.AttemptID, frozen.ClaimID); readErr != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, readErr
	} else if found {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, ErrConflict
	}
	admittedAt := now()
	if !validAuxiliaryReviewTime(admittedAt) {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, invalidWorkboard("auxiliary_review_admission_time")
	}
	admittedAt = admittedAt.UTC()
	if err = validateAuxiliaryReviewFrozenState(ctx, tx, frozen, admittedAt); err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	if err = enforceAuxiliaryReviewCapacity(ctx, tx, frozen.BoardID, frozen.CardID, reservation); err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	admissionID := newWorkboardID()
	if admissionID == "" {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, errors.New("secure identifier generation failed")
	}
	record, err := auxiliaryReviewAdmissionRecord(reservation, operationID, admissionID, admittedAt, reservationDigest)
	if err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	if err = insertAuxiliaryReviewAdmissionTx(ctx, tx, record); err != nil {
		if existing, found, readErr := readAuxiliaryReviewAdmissionForCandidate(ctx, tx, frozen.BoardID, frozen.CardID, frozen.AttemptID, frozen.ClaimID); readErr == nil && found && existing != record {
			return workboard.AuxiliaryReviewAdmissionRecord{}, false, ErrConflict
		}
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	fence, err := auxiliaryReviewSuccessorFence(record, frozen)
	if err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	if err = insertAuxiliaryReviewSuccessorFenceTx(ctx, tx, fence); err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	return record, true, nil
}

// ReplayAuxiliaryReviewAdmission reports whether an operation was durably
// admitted without granting dispatch authority.
func (s *Store) ReplayAuxiliaryReviewAdmission(ctx context.Context, operationID string) (workboard.AuxiliaryReviewAdmissionRecord, bool, error) {
	if ctx == nil || s == nil || s.db == nil || !validWorkboardID(operationID) {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, invalidWorkboard("auxiliary_review_admission")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	defer tx.Rollback()
	record, found, err := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operationID)
	if err != nil || !found {
		return workboard.AuxiliaryReviewAdmissionRecord{}, found, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	return record, true, nil
}

// SettleAuxiliaryReview writes one terminal accounting fact. Exact terminal
// retries are idempotent; changed measurements or disposition conflict.
func (s *Store) SettleAuxiliaryReview(ctx context.Context, operationID string, disposition workboard.AuxiliaryReviewDisposition,
	measurements workboard.AuxiliaryReviewMeasurements, settledAt time.Time,
) (workboard.AuxiliaryReviewSettlementRecord, bool, error) {
	if ctx == nil || s == nil || s.db == nil || !validWorkboardID(operationID) || !validAuxiliaryReviewTime(settledAt) ||
		!validAuxiliaryReviewDisposition(disposition) || !validAuxiliaryReviewMeasurements(measurements) {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, invalidWorkboard("auxiliary_review_settlement")
	}
	settledAt = settledAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	admission, found, err := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operationID)
	if err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	if !found {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, sql.ErrNoRows
	}
	if disposition == workboard.AuxiliaryReviewCompleted {
		legacy, legacyErr := isLegacyAuxiliaryReviewOutcomeAdmission(ctx, tx, admission)
		if legacyErr != nil {
			return workboard.AuxiliaryReviewSettlementRecord{}, false, legacyErr
		}
		if !legacy {
			return workboard.AuxiliaryReviewSettlementRecord{}, false, ErrConflict
		}
	}
	if prior, settled, readErr := readAuxiliaryReviewSettlement(ctx, tx, admission.AdmissionID); settled || readErr != nil {
		if readErr != nil {
			return workboard.AuxiliaryReviewSettlementRecord{}, false, readErr
		}
		want, buildErr := auxiliaryReviewSettlementRecord(admission, prior.SettlementID, disposition, measurements, prior.SettledAt)
		if buildErr != nil || prior != want {
			return workboard.AuxiliaryReviewSettlementRecord{}, false, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return workboard.AuxiliaryReviewSettlementRecord{}, false, err
		}
		return prior, false, nil
	}
	settlementID := newWorkboardID()
	if settlementID == "" {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, errors.New("secure identifier generation failed")
	}
	record, err := auxiliaryReviewSettlementRecord(admission, settlementID, disposition, measurements, settledAt)
	if err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	if err = insertAuxiliaryReviewSettlementTx(ctx, tx, record); err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	return record, true, nil
}

// ReplayAuxiliaryReviewSettlement returns the immutable terminal accounting
// fact without inventing new measurements. It is used to distinguish an
// acknowledged terminal review from the crash window after candidate commit.
func (s *Store) ReplayAuxiliaryReviewSettlement(ctx context.Context, operationID string,
	disposition workboard.AuxiliaryReviewDisposition,
) (workboard.AuxiliaryReviewSettlementRecord, bool, error) {
	if ctx == nil || s == nil || s.db == nil || !validWorkboardID(operationID) || !validAuxiliaryReviewDisposition(disposition) {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, invalidWorkboard("auxiliary_review_settlement")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	defer tx.Rollback()
	admission, found, err := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operationID)
	if err != nil || !found {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	record, found, err := readAuxiliaryReviewSettlement(ctx, tx, admission.AdmissionID)
	if err != nil || !found {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	if record.Disposition != disposition {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	return record, true, nil
}

func auxiliaryReviewMatchesFrozen(r workboard.AuxiliaryReviewReservation, f workboard.CandidateEvaluationRequest) bool {
	return r.BoardID == f.BoardID && r.CardID == f.CardID && r.AttemptID == f.AttemptID && r.ClaimID == f.ClaimID &&
		r.CandidateID == f.CandidateID && r.CandidateDigest == f.CandidateDigest && r.CriteriaDigest == f.CriteriaDigest &&
		r.PolicyDigest == f.PolicyDigest
}

func validAuxiliaryReviewTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && value.Year() >= 1970 && value.Year() < 2261 && offset == 0
}

func validAuxiliaryReviewDisposition(value workboard.AuxiliaryReviewDisposition) bool {
	return value == workboard.AuxiliaryReviewCompleted || value == workboard.AuxiliaryReviewFailed || value == workboard.AuxiliaryReviewCanceled
}

func validAuxiliaryReviewMeasurements(value workboard.AuxiliaryReviewMeasurements) bool {
	return validOptionalAuxiliaryReviewMeasurement(value.TimeMS, workboard.MaxAuxiliaryReviewDurationMillis) &&
		validOptionalAuxiliaryReviewMeasurement(value.Tokens, workboard.MaxWorkTokens) &&
		validOptionalAuxiliaryReviewMeasurement(value.CostMicros, workboard.MaxWorkCostMicros)
}

func validOptionalAuxiliaryReviewMeasurement(value *int64, maximum int64) bool {
	return value == nil || *value >= 0 && *value <= maximum
}

func validateAuxiliaryReviewFrozenState(ctx context.Context, tx *sql.Tx, frozen workboard.CandidateEvaluationRequest, now time.Time) error {
	board, _, _, err := readBoardRow(ctx, tx, frozen.BoardID)
	if err != nil {
		return err
	}
	card, _, err := readStoredCard(ctx, tx, frozen.BoardID, frozen.CardID)
	if err != nil {
		return err
	}
	if board.State != "active" || card.State != workboard.InProgress || card.Revision != frozen.ExpectedCardRevision ||
		card.CurrentAttemptID != frozen.AttemptID || card.CurrentClaimID != frozen.ClaimID || card.CriteriaRevision != frozen.CriteriaRevision ||
		card.CancelRequested || card.PausePhase == workboard.PauseAcknowledged || card.PausePhase == workboard.ResumeRequested {
		return &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "auxiliary_review_candidate"}
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, frozen.BoardID, frozen.CardID, frozen.AttemptID, frozen.ClaimID)
	if err != nil {
		return err
	}
	if lease.Revision != frozen.ExpectedClaimRevision || lease.State != workboard.LeaseActive || lease.OwnerID != frozen.WorkerID || !now.Before(lease.ExpiresAt) {
		return &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "auxiliary_review_claim"}
	}
	mutation := workboard.EvaluationMutation{Version: workboard.EvaluationMutationVersion, Kind: workboard.EvaluationCandidateSubmit,
		BoardID: frozen.BoardID, CardID: frozen.CardID, AttemptID: frozen.AttemptID, ClaimID: frozen.ClaimID,
		CandidateID: frozen.CandidateID, Actor: workboard.Actor{ID: frozen.WorkerID, Type: "worker"},
		ExpectedCardRevision: frozen.ExpectedCardRevision, ExpectedClaimRevision: frozen.ExpectedClaimRevision,
		CriteriaRevision: frozen.CriteriaRevision, CandidateDigest: frozen.CandidateDigest, CriteriaDigest: frozen.CriteriaDigest,
		PolicyDigest: frozen.PolicyDigest, Summary: frozen.Summary, ArtifactRefs: append([]string{}, frozen.ArtifactRefs...), Now: now}
	attempt, _, err := readRunningEvaluationAttempt(ctx, tx, mutation, claim)
	if err != nil {
		return err
	}
	if attempt.CriteriaDigest != frozen.CriteriaDigest || attempt.PolicyDigest != frozen.PolicyDigest ||
		workboard.AcceptanceCriteriaDigest(frozen.Criteria) != attempt.CriteriaDigest {
		return ErrWorkboardCorrupt
	}
	var candidates int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`,
		frozen.BoardID, frozen.CardID, frozen.AttemptID).Scan(&candidates); err != nil {
		return err
	}
	if candidates != 0 {
		return ErrConflict
	}
	return nil
}

func enforceAuxiliaryReviewCapacity(ctx context.Context, tx *sql.Tx, boardID, cardID string, reservation workboard.AuxiliaryReviewReservation) error {
	card, body, err := readStoredCard(ctx, tx, boardID, cardID)
	if err != nil {
		return err
	}
	budget := card.Budget
	if body.Budget != (storedWorkboardBudget{AttemptLimit: budget.AttemptLimit, TimeLimitMS: budget.TimeLimitMS,
		TokenLimit: budget.TokenLimit, CostMicros: budget.CostMicros}) {
		return ErrWorkboardCorrupt
	}
	if err = validateReservationBound("time_budget", budget.TimeLimitMS, reservation.TimeLimitMS, true); err != nil {
		return err
	}
	if err = validateReservationBound("token_budget", budget.TokenLimit, reservation.TokenLimit, true); err != nil {
		return err
	}
	if err = validateReservationBound("cost_budget", budget.CostMicros, reservation.CostMicros, false); err != nil {
		return err
	}
	executionAccounts, err := validatedExecutionAccounts(ctx, tx, boardID, cardID)
	if err != nil {
		return err
	}
	settled, unresolved, err := executionCardCharges(executionAccounts, boardID, cardID)
	if err != nil {
		return err
	}
	reviewSettled, reviewUnresolved, err := auxiliaryReviewCardCharges(ctx, tx, boardID, cardID)
	if err != nil {
		return err
	}
	if err = addExecutionCharge(&settled, reviewSettled.timeMS, reviewSettled.tokens, reviewSettled.costMicros); err != nil {
		return err
	}
	if err = addExecutionCharge(&unresolved, reviewUnresolved.timeMS, reviewUnresolved.tokens, reviewUnresolved.costMicros); err != nil {
		return err
	}
	if exceedsExecutionBudget(budget.TimeLimitMS, settled.timeMS, unresolved.timeMS, reservation.TimeLimitMS) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "time_budget"}
	}
	if exceedsExecutionBudget(budget.TokenLimit, settled.tokens, unresolved.tokens, reservation.TokenLimit) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "token_budget"}
	}
	if exceedsExecutionBudget(budget.CostMicros, settled.costMicros, unresolved.costMicros, reservation.CostMicros) {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "cost_budget"}
	}
	return nil
}

func auxiliaryReviewAdmissionRecord(r workboard.AuxiliaryReviewReservation, operationID, admissionID string, admittedAt time.Time,
	reservationDigest string,
) (workboard.AuxiliaryReviewAdmissionRecord, error) {
	record := workboard.AuxiliaryReviewAdmissionRecord{Version: workboard.SchemaVersion, AdmissionID: admissionID, OperationID: operationID,
		BoardID: r.BoardID, CardID: r.CardID, AttemptID: r.AttemptID, ClaimID: r.ClaimID, CandidateID: r.CandidateID,
		CandidateDigest: r.CandidateDigest, CriteriaDigest: r.CriteriaDigest, PolicyDigest: r.PolicyDigest,
		ReviewerID: r.ReviewerID, ModelID: r.ModelID, ProviderID: r.ProviderID, ConfigID: r.ConfigID,
		TimeLimitMS: r.TimeLimitMS, TokenLimit: r.TokenLimit, CostMicros: r.CostMicros, AdmittedAt: admittedAt.UTC(),
		ReservationDigest: reservationDigest}
	var err error
	record.AdmissionDigest, err = record.CanonicalDigest()
	if err != nil || record.Validate() != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, ErrWorkboardCorrupt
	}
	return record, nil
}

func insertAuxiliaryReviewAdmissionTx(ctx context.Context, tx *sql.Tx, r workboard.AuxiliaryReviewAdmissionRecord) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_auxiliary_review_admissions(admission_id,operation_id,board_id,card_id,attempt_id,
		claim_id,candidate_id,candidate_digest,criteria_digest,policy_digest,reviewer_id,model_id,provider_id,config_id,time_limit_ms,
		token_limit,cost_micros,admitted_at,reservation_digest,admission_digest,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.AdmissionID, r.OperationID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.CandidateID, r.CandidateDigest,
		r.CriteriaDigest, r.PolicyDigest, r.ReviewerID, r.ModelID, r.ProviderID, r.ConfigID, r.TimeLimitMS, r.TokenLimit,
		r.CostMicros, r.AdmittedAt.UnixNano(), r.ReservationDigest, r.AdmissionDigest, body)
	return err
}

func readAuxiliaryReviewAdmissionByOperation(ctx context.Context, tx *sql.Tx, operationID string) (workboard.AuxiliaryReviewAdmissionRecord, bool, error) {
	return readAuxiliaryReviewAdmission(ctx, tx, `operation_id=?`, operationID)
}

func readAuxiliaryReviewAdmissionForCandidate(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID, claimID string) (workboard.AuxiliaryReviewAdmissionRecord, bool, error) {
	return readAuxiliaryReviewAdmission(ctx, tx, `board_id=? AND card_id=? AND attempt_id=? AND claim_id=?`, boardID, cardID, attemptID, claimID)
}

func readAuxiliaryReviewAdmission(ctx context.Context, tx *sql.Tx, predicate string, args ...any) (workboard.AuxiliaryReviewAdmissionRecord, bool, error) {
	var indexed workboard.AuxiliaryReviewAdmissionRecord
	var admittedAt int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT admission_id,operation_id,board_id,card_id,attempt_id,claim_id,candidate_id,candidate_digest,
		criteria_digest,policy_digest,reviewer_id,model_id,provider_id,config_id,time_limit_ms,token_limit,cost_micros,admitted_at,
		reservation_digest,admission_digest,body FROM workboard_auxiliary_review_admissions WHERE `+predicate, args...).Scan(
		&indexed.AdmissionID, &indexed.OperationID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.ClaimID,
		&indexed.CandidateID, &indexed.CandidateDigest, &indexed.CriteriaDigest, &indexed.PolicyDigest, &indexed.ReviewerID,
		&indexed.ModelID, &indexed.ProviderID, &indexed.ConfigID, &indexed.TimeLimitMS, &indexed.TokenLimit, &indexed.CostMicros,
		&admittedAt, &indexed.ReservationDigest, &indexed.AdmissionDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, nil
	}
	if err != nil {
		return workboard.AuxiliaryReviewAdmissionRecord{}, false, err
	}
	indexed.Version, indexed.AdmittedAt = workboard.SchemaVersion, time.Unix(0, admittedAt).UTC()
	var canonical workboard.AuxiliaryReviewAdmissionRecord
	if strictJSON(body, &canonical) != nil || indexed.Validate() != nil || canonical.Validate() != nil || indexed != canonical {
		return workboard.AuxiliaryReviewAdmissionRecord{}, true, ErrWorkboardCorrupt
	}
	return canonical, true, nil
}

func auxiliaryReviewSettlementRecord(admission workboard.AuxiliaryReviewAdmissionRecord, settlementID string,
	disposition workboard.AuxiliaryReviewDisposition, measured workboard.AuxiliaryReviewMeasurements, settledAt time.Time,
) (workboard.AuxiliaryReviewSettlementRecord, error) {
	if admission.Validate() != nil || settledAt.Before(admission.AdmittedAt) {
		return workboard.AuxiliaryReviewSettlementRecord{}, invalidWorkboard("auxiliary_review_settlement")
	}
	timeCharge, timeMode := auxiliaryReviewCharge(measured.TimeMS, admission.TimeLimitMS)
	tokenCharge, tokenMode := auxiliaryReviewCharge(measured.Tokens, admission.TokenLimit)
	costCharge, costMode := auxiliaryReviewCharge(measured.CostMicros, admission.CostMicros)
	record := workboard.AuxiliaryReviewSettlementRecord{Version: workboard.SchemaVersion, SettlementID: settlementID,
		AdmissionID: admission.AdmissionID, AdmissionDigest: admission.AdmissionDigest, ReservationDigest: admission.ReservationDigest,
		OperationID: admission.OperationID, BoardID: admission.BoardID, CardID: admission.CardID, AttemptID: admission.AttemptID,
		ClaimID: admission.ClaimID, CandidateID: admission.CandidateID, CandidateDigest: admission.CandidateDigest,
		CriteriaDigest: admission.CriteriaDigest, PolicyDigest: admission.PolicyDigest, ReviewerID: admission.ReviewerID,
		ModelID: admission.ModelID, ProviderID: admission.ProviderID, ConfigID: admission.ConfigID,
		TimeLimitMS: admission.TimeLimitMS, TokenLimit: admission.TokenLimit, CostMicros: admission.CostMicros,
		AdmittedAt: admission.AdmittedAt, Disposition: disposition, ChargedTimeMS: timeCharge, ChargedTokens: tokenCharge,
		ChargedCostMicros: costCharge, TimeChargeMode: timeMode, TokenChargeMode: tokenMode, CostChargeMode: costMode,
		SettledAt: settledAt.UTC()}
	var err error
	record.SettlementDigest, err = record.CanonicalDigest()
	if err != nil || record.Validate() != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, invalidWorkboard("auxiliary_review_settlement")
	}
	return record, nil
}

func auxiliaryReviewCharge(measured *int64, reserved int64) (int64, workboard.AuxiliaryReviewChargeMode) {
	if measured == nil {
		return reserved, workboard.AuxiliaryReviewConservative
	}
	return *measured, workboard.AuxiliaryReviewMeasured
}

func insertAuxiliaryReviewSettlementTx(ctx context.Context, tx *sql.Tx, r workboard.AuxiliaryReviewSettlementRecord) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_auxiliary_review_settlements(settlement_id,admission_id,admission_digest,
		reservation_digest,operation_id,board_id,card_id,attempt_id,claim_id,candidate_id,candidate_digest,criteria_digest,
		policy_digest,reviewer_id,model_id,provider_id,config_id,time_limit_ms,token_limit,cost_micros,admitted_at,disposition,
		charged_time_ms,charged_tokens,charged_cost_micros,time_charge_mode,token_charge_mode,cost_charge_mode,settled_at,
		settlement_digest,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.SettlementID,
		r.AdmissionID, r.AdmissionDigest, r.ReservationDigest, r.OperationID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID,
		r.CandidateID, r.CandidateDigest, r.CriteriaDigest, r.PolicyDigest, r.ReviewerID, r.ModelID, r.ProviderID, r.ConfigID,
		r.TimeLimitMS, r.TokenLimit, r.CostMicros, r.AdmittedAt.UnixNano(), r.Disposition, r.ChargedTimeMS, r.ChargedTokens,
		r.ChargedCostMicros, r.TimeChargeMode, r.TokenChargeMode, r.CostChargeMode, r.SettledAt.UnixNano(), r.SettlementDigest, body)
	return err
}

func readAuxiliaryReviewSettlement(ctx context.Context, tx *sql.Tx, admissionID string) (workboard.AuxiliaryReviewSettlementRecord, bool, error) {
	var indexed workboard.AuxiliaryReviewSettlementRecord
	var admittedAt, settledAt int64
	var disposition, timeMode, tokenMode, costMode string
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT settlement_id,admission_id,admission_digest,reservation_digest,operation_id,board_id,card_id,
		attempt_id,claim_id,candidate_id,candidate_digest,criteria_digest,policy_digest,reviewer_id,model_id,provider_id,config_id,
		time_limit_ms,token_limit,cost_micros,admitted_at,disposition,charged_time_ms,charged_tokens,charged_cost_micros,
		time_charge_mode,token_charge_mode,cost_charge_mode,settled_at,settlement_digest,body
		FROM workboard_auxiliary_review_settlements WHERE admission_id=?`, admissionID).Scan(&indexed.SettlementID,
		&indexed.AdmissionID, &indexed.AdmissionDigest, &indexed.ReservationDigest, &indexed.OperationID, &indexed.BoardID,
		&indexed.CardID, &indexed.AttemptID, &indexed.ClaimID, &indexed.CandidateID, &indexed.CandidateDigest,
		&indexed.CriteriaDigest, &indexed.PolicyDigest, &indexed.ReviewerID, &indexed.ModelID, &indexed.ProviderID,
		&indexed.ConfigID, &indexed.TimeLimitMS, &indexed.TokenLimit, &indexed.CostMicros, &admittedAt, &disposition,
		&indexed.ChargedTimeMS, &indexed.ChargedTokens, &indexed.ChargedCostMicros, &timeMode, &tokenMode, &costMode,
		&settledAt, &indexed.SettlementDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, nil
	}
	if err != nil {
		return workboard.AuxiliaryReviewSettlementRecord{}, false, err
	}
	indexed.Version = workboard.SchemaVersion
	indexed.AdmittedAt, indexed.SettledAt = time.Unix(0, admittedAt).UTC(), time.Unix(0, settledAt).UTC()
	indexed.Disposition = workboard.AuxiliaryReviewDisposition(disposition)
	indexed.TimeChargeMode, indexed.TokenChargeMode, indexed.CostChargeMode = workboard.AuxiliaryReviewChargeMode(timeMode), workboard.AuxiliaryReviewChargeMode(tokenMode), workboard.AuxiliaryReviewChargeMode(costMode)
	var canonical workboard.AuxiliaryReviewSettlementRecord
	if strictJSON(body, &canonical) != nil || indexed.Validate() != nil || canonical.Validate() != nil || indexed != canonical {
		return workboard.AuxiliaryReviewSettlementRecord{}, true, ErrWorkboardCorrupt
	}
	return canonical, true, nil
}

func auxiliaryReviewCardCharges(ctx context.Context, tx *sql.Tx, boardID, cardID string) (executionCharges, executionCharges, error) {
	if err := validateCanonicalTriggerDefinitions(ctx, tx, canonicalAuxiliaryReviewAccountingGuardDefinitions()); err != nil {
		return executionCharges{}, executionCharges{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT admission_id FROM workboard_auxiliary_review_admissions
		WHERE board_id=? AND card_id=? ORDER BY admitted_at,admission_id`, boardID, cardID)
	if err != nil {
		return executionCharges{}, executionCharges{}, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil || !validWorkboardID(id) {
			return executionCharges{}, executionCharges{}, ErrWorkboardCorrupt
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return executionCharges{}, executionCharges{}, rows.Err()
	}
	var settled, unresolved executionCharges
	settlementCount := 0
	for _, id := range ids {
		admission, found, readErr := readAuxiliaryReviewAdmission(ctx, tx, `admission_id=?`, id)
		if readErr != nil || !found {
			if readErr != nil {
				return settled, unresolved, readErr
			}
			return settled, unresolved, ErrWorkboardCorrupt
		}
		settlement, terminal, readErr := readAuxiliaryReviewSettlement(ctx, tx, id)
		if readErr != nil {
			return settled, unresolved, readErr
		}
		if terminal {
			if !auxiliaryReviewSettlementMatchesAdmission(settlement, admission) {
				return settled, unresolved, ErrWorkboardCorrupt
			}
			settlementCount++
			readErr = addExecutionCharge(&settled, settlement.ChargedTimeMS, settlement.ChargedTokens, settlement.ChargedCostMicros)
		} else {
			readErr = addExecutionCharge(&unresolved, admission.TimeLimitMS, admission.TokenLimit, admission.CostMicros)
		}
		if readErr != nil {
			return settled, unresolved, readErr
		}
	}
	var storedSettlements int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_auxiliary_review_settlements WHERE board_id=? AND card_id=?`, boardID, cardID).Scan(&storedSettlements); err != nil {
		return settled, unresolved, err
	}
	if storedSettlements != settlementCount {
		return settled, unresolved, ErrWorkboardCorrupt
	}
	return settled, unresolved, nil
}

func auxiliaryReviewSettlementMatchesAdmission(s workboard.AuxiliaryReviewSettlementRecord, a workboard.AuxiliaryReviewAdmissionRecord) bool {
	return s.AdmissionID == a.AdmissionID && s.AdmissionDigest == a.AdmissionDigest && s.ReservationDigest == a.ReservationDigest &&
		s.OperationID == a.OperationID && s.BoardID == a.BoardID && s.CardID == a.CardID && s.AttemptID == a.AttemptID &&
		s.ClaimID == a.ClaimID && s.CandidateID == a.CandidateID && s.CandidateDigest == a.CandidateDigest &&
		s.CriteriaDigest == a.CriteriaDigest && s.PolicyDigest == a.PolicyDigest && s.ReviewerID == a.ReviewerID &&
		s.ModelID == a.ModelID && s.ProviderID == a.ProviderID && s.ConfigID == a.ConfigID &&
		s.TimeLimitMS == a.TimeLimitMS && s.TokenLimit == a.TokenLimit && s.CostMicros == a.CostMicros && s.AdmittedAt.Equal(a.AdmittedAt)
}

func canonicalAuxiliaryReviewAccountingGuardDefinitions() map[string]string {
	raw := auxiliaryReviewAccountingGuardSQL()
	for name, definition := range raw {
		raw[name] = normalizeTriggerDefinition(definition)
	}
	return raw
}

func auxiliaryReviewAccountingGuardSQL() map[string]string {
	return map[string]string{
		"workboard_auxiliary_review_admission_binding": `CREATE TRIGGER workboard_auxiliary_review_admission_binding BEFORE INSERT ON workboard_auxiliary_review_admissions
			WHEN NOT EXISTS(SELECT 1 FROM workboard_cards k
				JOIN workboard_attempts a ON a.board_id=k.board_id AND a.card_id=k.id
				JOIN workboard_claims c ON c.board_id=a.board_id AND c.card_id=a.card_id AND c.attempt_id=a.id
				WHERE k.board_id=NEW.board_id AND k.id=NEW.card_id AND k.state='in_progress'
				AND k.current_attempt_id=NEW.attempt_id AND k.current_claim_id=NEW.claim_id
				AND a.id=NEW.attempt_id AND a.state='running' AND a.criteria_digest=NEW.criteria_digest
				AND a.policy_digest=NEW.policy_digest AND c.id=NEW.claim_id AND c.state='active')
			BEGIN SELECT RAISE(ABORT,'workboard auxiliary review admission binding mismatch'); END`,
		"workboard_auxiliary_review_settlement_binding": `CREATE TRIGGER workboard_auxiliary_review_settlement_binding BEFORE INSERT ON workboard_auxiliary_review_settlements
			WHEN NOT EXISTS(SELECT 1 FROM workboard_auxiliary_review_admissions a
				WHERE a.admission_id=NEW.admission_id AND a.admission_digest=NEW.admission_digest
				AND a.reservation_digest=NEW.reservation_digest AND a.operation_id=NEW.operation_id
				AND a.board_id=NEW.board_id AND a.card_id=NEW.card_id AND a.attempt_id=NEW.attempt_id
				AND a.claim_id=NEW.claim_id AND a.candidate_id=NEW.candidate_id AND a.candidate_digest=NEW.candidate_digest
				AND a.criteria_digest=NEW.criteria_digest AND a.policy_digest=NEW.policy_digest
				AND a.reviewer_id=NEW.reviewer_id AND a.model_id=NEW.model_id AND a.provider_id=NEW.provider_id
				AND a.config_id=NEW.config_id AND a.time_limit_ms=NEW.time_limit_ms
				AND a.token_limit=NEW.token_limit AND a.cost_micros=NEW.cost_micros AND a.admitted_at=NEW.admitted_at)
			OR (NEW.disposition='completed' AND NOT EXISTS(SELECT 1 FROM workboard_candidates c
				WHERE c.board_id=NEW.board_id AND c.card_id=NEW.card_id AND c.attempt_id=NEW.attempt_id
				AND c.id=NEW.candidate_id AND c.digest=NEW.candidate_digest
				AND c.criteria_digest=NEW.criteria_digest AND c.policy_digest=NEW.policy_digest))
			BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement binding mismatch'); END`,
		"workboard_auxiliary_review_admission_immutable_update":  `CREATE TRIGGER workboard_auxiliary_review_admission_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_admissions BEGIN SELECT RAISE(ABORT,'workboard auxiliary review admission is immutable'); END`,
		"workboard_auxiliary_review_admission_immutable_delete":  `CREATE TRIGGER workboard_auxiliary_review_admission_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_admissions BEGIN SELECT RAISE(ABORT,'workboard auxiliary review admission is immutable'); END`,
		"workboard_auxiliary_review_settlement_immutable_update": `CREATE TRIGGER workboard_auxiliary_review_settlement_immutable_update BEFORE UPDATE ON workboard_auxiliary_review_settlements BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement is immutable'); END`,
		"workboard_auxiliary_review_settlement_immutable_delete": `CREATE TRIGGER workboard_auxiliary_review_settlement_immutable_delete BEFORE DELETE ON workboard_auxiliary_review_settlements BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement is immutable'); END`,
		"workboard_auxiliary_review_settlement_charge_limit":     `CREATE TRIGGER workboard_auxiliary_review_settlement_charge_limit BEFORE INSERT ON workboard_auxiliary_review_settlements WHEN NEW.charged_time_ms>300000 BEGIN SELECT RAISE(ABORT,'workboard auxiliary review settlement charge exceeds limit'); END`,
	}
}
