package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func finalizeStoredCancel(ctx context.Context, tx *sql.Tx, mutation workboard.ControlMutation, board *workboard.Board, card *workboard.Card, body *storedWorkboardCard) (int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if !card.CancelRequested || card.State != workboard.InProgress && card.State != workboard.Blocked ||
		card.CurrentAttemptID != mutation.AttemptID || card.CurrentClaimID != mutation.ClaimID {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "cancel"}
	}
	if mutation.Stop == nil || mutation.Verified == nil {
		return 0, 0, &workboard.Violation{Code: workboard.CodeUnsafeRecovery, Field: "proof"}
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID)
	if err != nil {
		return 0, 0, err
	}
	result, err := workboard.ApplyRecovery(lease, workboard.Recovery{ExpectedRevision: mutation.ExpectedClaimRevision, Now: mutation.Now, Proof: *mutation.Verified})
	if err != nil {
		return 0, 0, err
	}
	oldClaim := claim
	claim = updateStoredClaim(claim, result.Lease)
	claimBytes, err := encodeLifecycle(claim, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	attempt, oldAttemptBytes, err := readControlAttempt(ctx, tx, mutation, oldClaim)
	if err != nil {
		return 0, 0, err
	}
	attempt.Revision++
	attempt.State = "canceled"
	attempt.Claim = claim
	attempt.EndedAt = &mutation.Now
	attemptBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, 0, err
	}
	claimUpdate, err := tx.ExecContext(ctx, `UPDATE workboard_claims SET revision=?,state='released',released_at=?,body=? WHERE id=? AND revision=?`,
		claim.Revision, mutation.Now.UnixNano(), claimBytes, claim.ID, mutation.ExpectedClaimRevision)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := claimUpdate.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	attemptUpdate, err := tx.ExecContext(ctx, `UPDATE workboard_attempts SET revision=?,state='canceled',ended_at=?,body=? WHERE id=? AND revision=? AND state='running'`,
		attempt.Revision, mutation.Now.UnixNano(), attemptBytes, attempt.ID, attempt.Revision-1)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := attemptUpdate.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	body.State, body.CurrentClaimID, body.BlockReason = string(workboard.Canceled), "", ""
	body.CancelRequested, body.PauseRequested = false, false
	card.State, card.CurrentClaimID, card.BlockReason = workboard.Canceled, "", ""
	card.CancelRequested, card.PauseRequested = false, false
	card.Rank, err = appendRank(ctx, tx, board.ID, workboard.Canceled)
	if err != nil {
		return 0, 0, err
	}
	card.Revision++
	card.UpdatedAt = mutation.Now
	board.ActiveClaims--
	board.LayoutRevision++
	if board.ActiveClaims < 0 {
		return 0, 0, ErrWorkboardCorrupt
	}
	cardBytes, err := writeLifecycleCard(ctx, tx, *card, *body, mutation.ExpectedCardRevision)
	if err != nil {
		return 0, 0, err
	}
	proof := storedRecoveryProof{Version: 1, ID: mutation.Stop.StopProofID, BoardID: mutation.BoardID, CardID: mutation.CardID,
		AttemptID: mutation.AttemptID, ClaimID: mutation.ClaimID, TaskHeadDigest: mutation.Stop.TaskHeadDigest,
		ProcessProofDigest: mutation.Stop.ProcessProofDigest, EffectEvidenceDigest: mutation.Stop.EffectEvidenceDigest,
		EffectResolution: string(mutation.Stop.EffectResolution), CreatedAt: mutation.Now}
	proofBytes, err := encodeLifecycle(proof, 64<<10)
	if err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_recovery_proofs(id,board_id,card_id,attempt_id,claim_id,task_head_digest,process_proof_digest,effect_evidence_digest,effect_resolution,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, proof.ID, proof.BoardID, proof.CardID, proof.AttemptID, proof.ClaimID, proof.TaskHeadDigest,
		proof.ProcessProofDigest, proof.EffectEvidenceDigest, proof.EffectResolution, proof.CreatedAt.UnixNano(), proofBytes); err != nil {
		return 0, 0, normalizeLifecycleWriteError(err)
	}
	return claim.Revision, len(claimBytes) + len(oldAttemptBytes) + len(attemptBytes) + cardBytes + len(proofBytes), nil
}

func readControlAttempt(ctx context.Context, tx *sql.Tx, mutation workboard.ControlMutation, claim storedLifecycleClaim) (storedLifecycleAttempt, []byte, error) {
	var attempt storedLifecycleAttempt
	var revision int64
	var state, workerID, criteriaDigestValue, policyDigest string
	var endedAt sql.NullInt64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,state,worker_id,criteria_digest,policy_digest,ended_at,body FROM workboard_attempts
		WHERE board_id=? AND card_id=? AND id=?`, mutation.BoardID, mutation.CardID, mutation.AttemptID).
		Scan(&revision, &state, &workerID, &criteriaDigestValue, &policyDigest, &endedAt, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return storedLifecycleAttempt{}, nil, ErrWorkboardNotFound
	}
	if err != nil {
		return storedLifecycleAttempt{}, nil, err
	}
	if strictJSON(body, &attempt) != nil || attempt.Revision != revision || attempt.State != state || attempt.WorkerID != workerID ||
		attempt.CriteriaDigest != criteriaDigestValue || attempt.PolicyDigest != policyDigest || endedAt.Valid || attempt.State != "running" ||
		attempt.ID != mutation.AttemptID || attempt.BoardID != mutation.BoardID || attempt.CardID != mutation.CardID || !equalStoredClaim(attempt.Claim, claim) {
		return storedLifecycleAttempt{}, nil, ErrWorkboardCorrupt
	}
	return attempt, body, nil
}

func validateControlMutation(m workboard.ControlMutation, applying bool) error {
	if m.Version != workboard.ControlMutationVersion || !validWorkboardID(m.BoardID) || !validWorkboardID(m.CardID) || m.Actor.Validate() != nil ||
		len(m.IdempotencyKey) < 16 || len(m.IdempotencyKey) > 128 || !validDigest(m.RequestDigest) ||
		m.Now.Location() != time.UTC || m.Now.Year() < 1970 || m.Now.Year() >= 2261 {
		return invalidWorkboard("mutation")
	}
	for _, r := range m.IdempotencyKey {
		if r < 0x21 || r > 0x7e {
			return invalidWorkboard("mutation")
		}
	}
	switch m.Kind {
	case workboard.ControlPauseRequest, workboard.ControlCancelRequest:
		if m.Actor.Type != "operator" || m.AttemptID != "" || m.ClaimID != "" || m.ExpectedCardRevision < 1 ||
			m.ExpectedClaimRevision != 0 || m.ReasonCode != "" || m.Stop != nil || m.Verified != nil {
			return invalidWorkboard("control_request")
		}
	case workboard.ControlBlock, workboard.ControlUnblock:
		if m.Actor.Type != "worker" || !validWorkboardID(m.AttemptID) || !validWorkboardID(m.ClaimID) || m.ExpectedCardRevision < 1 ||
			m.ExpectedClaimRevision < 1 || !validWorkboardID(m.ReasonCode) || m.Stop != nil || m.Verified != nil {
			return invalidWorkboard("claim_control")
		}
	case workboard.ControlCancelFinalize:
		if m.Actor.Type != "operator" || !validWorkboardID(m.AttemptID) || !validWorkboardID(m.ClaimID) || m.ExpectedCardRevision < 1 ||
			m.ExpectedClaimRevision < 1 || m.ReasonCode != "" || m.Stop == nil || !validRecoveryIntent(*m.Stop) ||
			applying && !validControlProof(m) {
			return invalidWorkboard("cancel_finalize")
		}
	default:
		return invalidWorkboard("kind")
	}
	return nil
}

func validControlProof(m workboard.ControlMutation) bool {
	if m.Verified == nil || m.Stop == nil {
		return false
	}
	p, i := m.Verified, m.Stop
	return p.TaskTerminal && p.ProcessStopped && p.StopProofID == i.StopProofID && p.TaskHeadDigest == i.TaskHeadDigest &&
		p.ProcessProofDigest == i.ProcessProofDigest && p.EffectEvidenceDigest == i.EffectEvidenceDigest && p.EffectResolution == i.EffectResolution
}

func controlAction(kind workboard.ControlKind) workboard.BoardAction {
	switch kind {
	case workboard.ControlPauseRequest:
		return workboard.CardPauseRequestAction
	case workboard.ControlCancelRequest:
		return workboard.CardCancelRequestAction
	case workboard.ControlCancelFinalize:
		return workboard.CardCancelFinalizeAction
	case workboard.ControlBlock:
		return workboard.CardBlockAction
	case workboard.ControlUnblock:
		return workboard.CardUnblockAction
	default:
		return ""
	}
}
