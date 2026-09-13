package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// RecoverExpiredAuxiliaryReview terminalizes an unresolved review whose
// immutable completion deadline has elapsed. Recovery never grants reviewer
// dispatch or completion authority and, because terminal measurements were
// lost with the process, charges every admitted reservation conservatively.
func (s *Store) RecoverExpiredAuxiliaryReview(ctx context.Context, operationID string, now func() time.Time) (bool, error) {
	if ctx == nil || s == nil || s.db == nil || !validWorkboardID(operationID) || now == nil {
		return false, invalidWorkboard("auxiliary_review_recovery")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return false, err
	}
	admission, found, err := readAuxiliaryReviewAdmissionByOperation(ctx, tx, operationID)
	if err != nil || !found {
		return false, err
	}
	fence, fenceFound, err := readAuxiliaryReviewSuccessorFence(ctx, tx, admission.AdmissionID)
	if err != nil {
		return false, err
	}
	legacy, err := isLegacyAuxiliaryReviewAdmission(ctx, tx, admission)
	if err != nil {
		return false, err
	}
	deadline := admission.AdmittedAt.Add(time.Duration(admission.TimeLimitMS) * time.Millisecond)
	if fenceFound {
		if !recoveryFenceMatchesAdmission(fence, admission, deadline) {
			return false, ErrWorkboardCorrupt
		}
		deadline = fence.DeadlineAt
	} else if !legacy {
		return false, ErrWorkboardCorrupt
	}

	settlement, settled, err := readAuxiliaryReviewSettlement(ctx, tx, admission.AdmissionID)
	if err != nil {
		return false, err
	}
	outcome, outcomeFound, err := readAuxiliaryReviewOutcome(ctx, tx, admission.AdmissionID)
	if err != nil {
		return false, err
	}
	var candidates int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_candidates
		WHERE board_id=? AND card_id=? AND attempt_id=?`, admission.BoardID, admission.CardID, admission.AttemptID).Scan(&candidates); err != nil {
		return false, err
	}
	if settled {
		if !recoverySettlementMatchesAdmission(settlement, admission) {
			return false, ErrWorkboardCorrupt
		}
		if settlement.Disposition == workboard.AuxiliaryReviewCompleted {
			if legacy {
				legacyOutcome, legacyErr := isLegacyAuxiliaryReviewOutcomeAdmission(ctx, tx, admission)
				if legacyErr != nil {
					return false, legacyErr
				}
				if outcomeFound || !legacyOutcome || candidates != 1 || validateLegacyAuxiliaryReviewCompletion(ctx, tx, admission) != nil {
					return false, ErrWorkboardCorrupt
				}
			} else if !outcomeFound {
				return false, ErrWorkboardCorrupt
			} else if _, err = validateAuxiliaryReviewOutcomeGraph(ctx, tx, admission, settlement, outcome); err != nil {
				return false, err
			}
		} else if outcomeFound || candidates != 0 {
			return false, ErrWorkboardCorrupt
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if outcomeFound || candidates != 0 {
		return false, ErrWorkboardCorrupt
	}
	recoveredAt := now()
	if !validAuxiliaryReviewTime(recoveredAt) {
		return false, invalidWorkboard("auxiliary_review_recovery_time")
	}
	recoveredAt = recoveredAt.UTC()
	if recoveredAt.Before(deadline) {
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	settlementID := newWorkboardID()
	if settlementID == "" {
		return false, errors.New("secure identifier generation failed")
	}
	recovered, err := auxiliaryReviewSettlementRecord(admission, settlementID, workboard.AuxiliaryReviewFailed,
		workboard.AuxiliaryReviewMeasurements{}, recoveredAt)
	if err != nil {
		return false, err
	}
	if err = insertAuxiliaryReviewSettlementTx(ctx, tx, recovered); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func validateLegacyAuxiliaryReviewCompletion(ctx context.Context, tx *sql.Tx,
	admission workboard.AuxiliaryReviewAdmissionRecord,
) error {
	candidate, err := readEvaluationCandidate(ctx, tx, admission.BoardID, admission.CardID, admission.AttemptID)
	if err != nil || candidate.ID != admission.CandidateID || candidate.Digest != admission.CandidateDigest ||
		candidate.CriteriaDigest != admission.CriteriaDigest || candidate.PolicyDigest != admission.PolicyDigest {
		return ErrWorkboardCorrupt
	}
	evidence, err := readEvaluationEvidence(ctx, tx, admission.BoardID, admission.CardID, admission.AttemptID, candidate)
	if err != nil || len(evidence) < candidate.EvidenceCount ||
		workboard.EvidenceSetDigest(evidence[:candidate.EvidenceCount]) != candidate.EvidenceDigest {
		return ErrWorkboardCorrupt
	}
	return nil
}

func recoverySettlementMatchesAdmission(settlement workboard.AuxiliaryReviewSettlementRecord,
	admission workboard.AuxiliaryReviewAdmissionRecord,
) bool {
	return settlement.Validate() == nil && admission.Validate() == nil &&
		settlement.AdmissionID == admission.AdmissionID && settlement.AdmissionDigest == admission.AdmissionDigest &&
		settlement.ReservationDigest == admission.ReservationDigest && settlement.OperationID == admission.OperationID &&
		settlement.BoardID == admission.BoardID && settlement.CardID == admission.CardID &&
		settlement.AttemptID == admission.AttemptID && settlement.ClaimID == admission.ClaimID &&
		settlement.CandidateID == admission.CandidateID && settlement.CandidateDigest == admission.CandidateDigest &&
		settlement.CriteriaDigest == admission.CriteriaDigest && settlement.PolicyDigest == admission.PolicyDigest &&
		settlement.ReviewerID == admission.ReviewerID && settlement.ModelID == admission.ModelID &&
		settlement.ProviderID == admission.ProviderID && settlement.ConfigID == admission.ConfigID &&
		settlement.TimeLimitMS == admission.TimeLimitMS && settlement.TokenLimit == admission.TokenLimit &&
		settlement.CostMicros == admission.CostMicros && settlement.AdmittedAt.Equal(admission.AdmittedAt)
}

func recoveryFenceMatchesAdmission(fence workboard.AuxiliaryReviewSuccessorFence,
	admission workboard.AuxiliaryReviewAdmissionRecord, deadline time.Time,
) bool {
	return fence.Validate() == nil && admission.Validate() == nil && fence.AdmissionID == admission.AdmissionID &&
		fence.AdmissionDigest == admission.AdmissionDigest && fence.OperationID == admission.OperationID &&
		fence.BoardID == admission.BoardID && fence.CardID == admission.CardID && fence.AttemptID == admission.AttemptID &&
		fence.ClaimID == admission.ClaimID && fence.CandidateID == admission.CandidateID &&
		fence.CandidateDigest == admission.CandidateDigest && fence.CriteriaDigest == admission.CriteriaDigest &&
		fence.PolicyDigest == admission.PolicyDigest && fence.AdmittedAt.Equal(admission.AdmittedAt) &&
		fence.DeadlineAt.Equal(deadline)
}
