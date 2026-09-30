package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func auxiliaryReviewSuccessorFence(admission workboard.AuxiliaryReviewAdmissionRecord,
	frozen workboard.CandidateEvaluationRequest,
) (workboard.AuxiliaryReviewSuccessorFence, error) {
	if admission.Validate() != nil || frozen.Validate() != nil || admission.BoardID != frozen.BoardID ||
		admission.CardID != frozen.CardID || admission.AttemptID != frozen.AttemptID || admission.ClaimID != frozen.ClaimID ||
		admission.CandidateID != frozen.CandidateID || admission.CandidateDigest != frozen.CandidateDigest ||
		admission.CriteriaDigest != frozen.CriteriaDigest || admission.PolicyDigest != frozen.PolicyDigest {
		return workboard.AuxiliaryReviewSuccessorFence{}, ErrWorkboardCorrupt
	}
	fence := workboard.AuxiliaryReviewSuccessorFence{
		Version: workboard.AuxiliaryReviewSuccessorFenceVersion, AdmissionID: admission.AdmissionID,
		AdmissionDigest: admission.AdmissionDigest, OperationID: admission.OperationID,
		BoardID: admission.BoardID, CardID: admission.CardID, AttemptID: admission.AttemptID, ClaimID: admission.ClaimID,
		CardRevision: frozen.ExpectedCardRevision, ClaimRevision: frozen.ExpectedClaimRevision,
		CriteriaRevision: frozen.CriteriaRevision, CandidateID: admission.CandidateID,
		CandidateDigest: admission.CandidateDigest, CriteriaDigest: admission.CriteriaDigest, PolicyDigest: admission.PolicyDigest,
		AdmittedAt: admission.AdmittedAt, DeadlineAt: admission.AdmittedAt.Add(time.Duration(admission.TimeLimitMS) * time.Millisecond),
	}
	var err error
	fence.FenceDigest, err = fence.CanonicalDigest()
	if err != nil || fence.Validate() != nil {
		return workboard.AuxiliaryReviewSuccessorFence{}, ErrWorkboardCorrupt
	}
	return fence, nil
}

func insertAuxiliaryReviewSuccessorFenceTx(ctx context.Context, tx *sql.Tx, f workboard.AuxiliaryReviewSuccessorFence) error {
	if f.Validate() != nil {
		return invalidWorkboard("auxiliary_review_successor_fence")
	}
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_auxiliary_review_successor_fences(admission_id,admission_digest,operation_id,
		board_id,card_id,attempt_id,claim_id,card_revision,claim_revision,criteria_revision,candidate_id,candidate_digest,
		criteria_digest,policy_digest,admitted_at,deadline_at,fence_digest,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.AdmissionID, f.AdmissionDigest, f.OperationID, f.BoardID, f.CardID, f.AttemptID, f.ClaimID, f.CardRevision,
		f.ClaimRevision, f.CriteriaRevision, f.CandidateID, f.CandidateDigest, f.CriteriaDigest, f.PolicyDigest,
		f.AdmittedAt.UnixNano(), f.DeadlineAt.UnixNano(), f.FenceDigest, body)
	return err
}

// AuxiliaryReviewSuccessorFence returns the immutable completion authority
// snapshot for an admission. A schema-43 admission returns found=false and is
// never promoted by this read.
func (s *Store) AuxiliaryReviewSuccessorFence(ctx context.Context, admissionID string) (workboard.AuxiliaryReviewSuccessorFence, bool, error) {
	if ctx == nil || s == nil || s.db == nil || !validWorkboardID(admissionID) {
		return workboard.AuxiliaryReviewSuccessorFence{}, false, invalidWorkboard("auxiliary_review_successor_fence")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AuxiliaryReviewSuccessorFence{}, false, err
	}
	defer tx.Rollback()
	fence, found, err := readAuxiliaryReviewSuccessorFence(ctx, tx, admissionID)
	if err != nil || !found {
		return workboard.AuxiliaryReviewSuccessorFence{}, found, err
	}
	return fence, true, tx.Commit()
}

func readAuxiliaryReviewSuccessorFence(ctx context.Context, tx *sql.Tx, admissionID string) (workboard.AuxiliaryReviewSuccessorFence, bool, error) {
	var indexed workboard.AuxiliaryReviewSuccessorFence
	var admittedAt, deadlineAt int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT admission_id,admission_digest,operation_id,board_id,card_id,attempt_id,claim_id,
		card_revision,claim_revision,criteria_revision,candidate_id,candidate_digest,criteria_digest,policy_digest,
		admitted_at,deadline_at,fence_digest,body FROM workboard_auxiliary_review_successor_fences WHERE admission_id=?`, admissionID).Scan(
		&indexed.AdmissionID, &indexed.AdmissionDigest, &indexed.OperationID, &indexed.BoardID, &indexed.CardID,
		&indexed.AttemptID, &indexed.ClaimID, &indexed.CardRevision, &indexed.ClaimRevision, &indexed.CriteriaRevision,
		&indexed.CandidateID, &indexed.CandidateDigest, &indexed.CriteriaDigest, &indexed.PolicyDigest,
		&admittedAt, &deadlineAt, &indexed.FenceDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AuxiliaryReviewSuccessorFence{}, false, nil
	}
	if err != nil {
		return workboard.AuxiliaryReviewSuccessorFence{}, false, err
	}
	indexed.Version = workboard.AuxiliaryReviewSuccessorFenceVersion
	indexed.AdmittedAt, indexed.DeadlineAt = time.Unix(0, admittedAt).UTC(), time.Unix(0, deadlineAt).UTC()
	var canonical workboard.AuxiliaryReviewSuccessorFence
	if strictJSON(body, &canonical) != nil || indexed.Validate() != nil || canonical.Validate() != nil || indexed != canonical {
		return workboard.AuxiliaryReviewSuccessorFence{}, true, ErrWorkboardCorrupt
	}
	return canonical, true, nil
}

func isLegacyAuxiliaryReviewAdmission(ctx context.Context, tx *sql.Tx, admission workboard.AuxiliaryReviewAdmissionRecord) (bool, error) {
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT admission_digest FROM workboard_auxiliary_review_legacy_admissions WHERE admission_id=?`, admission.AdmissionID).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if digest != admission.AdmissionDigest {
		return false, ErrWorkboardCorrupt
	}
	return true, nil
}
