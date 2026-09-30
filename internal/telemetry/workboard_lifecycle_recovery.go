package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func applyClaimRecovery(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation, board *workboard.Board, card *workboard.Card, body *storedWorkboardCard) (int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.CurrentClaimID != mutation.ClaimID || body.CurrentAttemptID != mutation.AttemptID ||
		card.State != workboard.InProgress && card.State != workboard.Blocked {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "claim"}
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID)
	if err != nil {
		return 0, 0, err
	}
	if mutation.Verified == nil || mutation.Recovery == nil {
		return 0, 0, &workboard.Violation{Code: workboard.CodeUnsafeRecovery, Field: "proof"}
	}
	result, err := workboard.ApplyRecovery(lease, workboard.Recovery{ExpectedRevision: mutation.ExpectedClaimRevision, Now: mutation.Now, Proof: *mutation.Verified})
	if err != nil {
		return 0, 0, err
	}
	claim = updateStoredClaim(claim, result.Lease)
	claimBytes, err := encodeLifecycle(claim, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	var attemptBytes []byte
	var attempt storedLifecycleAttempt
	var indexedRevision int64
	var indexedState, workerID, criteriaDigest, policyDigest string
	var endedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT revision,state,worker_id,criteria_digest,policy_digest,ended_at,body FROM workboard_attempts
		WHERE board_id=? AND card_id=? AND id=?`, mutation.BoardID, mutation.CardID, mutation.AttemptID).
		Scan(&indexedRevision, &indexedState, &workerID, &criteriaDigest, &policyDigest, &endedAt, &attemptBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrWorkboardNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	if strictJSON(attemptBytes, &attempt) != nil || indexedRevision != attempt.Revision || indexedState != attempt.State || workerID != attempt.WorkerID ||
		criteriaDigest != attempt.CriteriaDigest || policyDigest != attempt.PolicyDigest || endedAt.Valid || attempt.ID != mutation.AttemptID ||
		attempt.BoardID != mutation.BoardID || attempt.CardID != mutation.CardID || attempt.State != "running" || !equalStoredClaim(attempt.Claim, updateStoredClaim(claim, lease)) {
		return 0, 0, ErrWorkboardCorrupt
	}
	attempt.Revision++
	attempt.State = "failed"
	attempt.Claim = claim
	attempt.EndedAt = &mutation.Now
	nextAttemptBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
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
	attemptUpdate, err := tx.ExecContext(ctx, `UPDATE workboard_attempts SET revision=?,state='failed',ended_at=?,body=? WHERE id=? AND revision=? AND state='running'`,
		attempt.Revision, mutation.Now.UnixNano(), nextAttemptBytes, attempt.ID, attempt.Revision-1)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := attemptUpdate.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	body.State, body.CurrentClaimID, body.AssigneeID = string(workboard.Ready), "", ""
	body.BlockReason, body.CancelRequested, body.PauseRequested, body.PausePhase = "", false, false, workboard.PauseNone
	card.State, card.CurrentClaimID, card.AssigneeID = workboard.Ready, "", ""
	card.CancelRequested, card.PauseRequested, card.PausePhase = false, false, workboard.PauseNone
	card.Rank, err = appendRank(ctx, tx, board.ID, workboard.Ready)
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
	proof := storedRecoveryProof{Version: 1, ID: mutation.Recovery.StopProofID, BoardID: mutation.BoardID, CardID: mutation.CardID,
		AttemptID: mutation.AttemptID, ClaimID: mutation.ClaimID, TaskHeadDigest: mutation.Recovery.TaskHeadDigest,
		ProcessProofDigest: mutation.Recovery.ProcessProofDigest, EffectEvidenceDigest: mutation.Recovery.EffectEvidenceDigest,
		EffectResolution: string(mutation.Recovery.EffectResolution), CreatedAt: mutation.Now}
	proofBytes, err := encodeLifecycle(proof, 64<<10)
	if err != nil {
		return 0, 0, err
	}
	recoveryID := newWorkboardID()
	if recoveryID == "" {
		return 0, 0, errors.New("secure identifier generation failed")
	}
	recovery := workboard.RecoveryRecord{Version: 1, ID: recoveryID, BoardID: mutation.BoardID, CardID: mutation.CardID, AttemptID: mutation.AttemptID,
		OldClaimID: mutation.ClaimID, OldClaimRevision: claim.Revision, CardRevision: card.Revision, StopProofID: proof.ID,
		TaskHeadDigest: proof.TaskHeadDigest, ProcessProofDigest: proof.ProcessProofDigest, EffectEvidenceDigest: proof.EffectEvidenceDigest,
		EffectResolution: workboard.EffectResolution(proof.EffectResolution), ResultingState: workboard.Ready, FirstSequence: board.EventSequence + 1,
		LastSequence: board.EventSequence + 1, RecoveredAt: mutation.Now}
	if recovery.Validate() != nil {
		return 0, 0, ErrWorkboardCorrupt
	}
	recoveryBytes, err := encodeLifecycle(recovery, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_recovery_proofs(id,board_id,card_id,attempt_id,claim_id,task_head_digest,process_proof_digest,effect_evidence_digest,effect_resolution,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, proof.ID, proof.BoardID, proof.CardID, proof.AttemptID, proof.ClaimID, proof.TaskHeadDigest,
		proof.ProcessProofDigest, proof.EffectEvidenceDigest, proof.EffectResolution, proof.CreatedAt.UnixNano(), proofBytes); err != nil {
		return 0, 0, normalizeLifecycleWriteError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_recoveries(id,proof_id,board_id,card_id,attempt_id,old_claim_id,old_claim_revision,card_revision,first_sequence,last_sequence,recovered_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, recovery.ID, recovery.StopProofID, recovery.BoardID, recovery.CardID, recovery.AttemptID,
		recovery.OldClaimID, recovery.OldClaimRevision, recovery.CardRevision, recovery.FirstSequence, recovery.LastSequence, recovery.RecoveredAt.UnixNano(), recoveryBytes); err != nil {
		return 0, 0, normalizeLifecycleWriteError(err)
	}
	settlementBytes, err := settleExecutionAttempt(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID, "", false, mutation.Now)
	if err != nil {
		return 0, 0, err
	}
	return claim.Revision, len(claimBytes) + len(attemptBytes) + len(nextAttemptBytes) + cardBytes + len(proofBytes) + len(recoveryBytes) + settlementBytes, nil
}

type storedRecoveryProof struct {
	Version              int       `json:"version"`
	ID                   string    `json:"id"`
	BoardID              string    `json:"board_id"`
	CardID               string    `json:"card_id"`
	AttemptID            string    `json:"attempt_id"`
	ClaimID              string    `json:"claim_id"`
	TaskHeadDigest       string    `json:"task_head_digest"`
	ProcessProofDigest   string    `json:"process_proof_digest"`
	EffectEvidenceDigest string    `json:"effect_evidence_digest"`
	EffectResolution     string    `json:"effect_resolution"`
	CreatedAt            time.Time `json:"created_at"`
}

func writeLifecycleCard(ctx context.Context, tx *sql.Tx, card workboard.Card, body storedWorkboardCard, expectedRevision int64) (int, error) {
	body = updateStoredBody(body, card)
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	if err = card.Validate(); err != nil {
		return 0, err
	}
	if !validStoredCardReferences(body) {
		return 0, ErrWorkboardCorrupt
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_cards SET revision=?,state=?,rank=?,assignee_id=?,block_reason=?,attempt_count=?,current_attempt_id=?,current_claim_id=?,
		cancel_requested=?,pause_requested=?,updated_at=?,body=? WHERE board_id=? AND id=? AND revision=?`, card.Revision, card.State,
		card.Rank, nullable(body.AssigneeID), nullable(body.BlockReason), body.AttemptCount, nullable(body.CurrentAttemptID), nullable(body.CurrentClaimID),
		boolInt(body.CancelRequested), boolInt(body.PauseRequested), card.UpdatedAt.UnixNano(), bodyBytes, card.BoardID, card.ID, expectedRevision)
	if err != nil {
		return 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	return len(bodyBytes), nil
}

func validateLifecycleMutation(m workboard.LifecycleMutation, applying bool) error {
	if m.Version != workboard.LifecycleMutationVersion || !validWorkboardID(m.BoardID) || !validWorkboardID(m.CardID) || m.Actor.Validate() != nil ||
		len(m.IdempotencyKey) < 16 || len(m.IdempotencyKey) > 128 || !validDigest(m.RequestDigest) {
		return invalidWorkboard("mutation")
	}
	for _, r := range m.IdempotencyKey {
		if r < 0x21 || r > 0x7e {
			return invalidWorkboard("mutation")
		}
	}
	if m.Now.Location() != time.UTC || m.Now.Year() < 1970 || m.Now.Year() >= 2261 {
		return invalidWorkboard("time")
	}
	switch m.Kind {
	case workboard.LifecycleClaim:
		if m.Actor.Type != "worker" || m.ExpectedCardRevision < 1 || m.AttemptID != "" || m.ClaimID != "" ||
			m.ExpectedClaimRevision != 0 || m.LeaseTTL < workboard.MinLeaseTTL || m.LeaseTTL > workboard.MaxLeaseTTL || !validDigest(m.PolicyDigest) || m.Recovery != nil ||
			m.EffectResolution != "" || (m.TaskID != "" || m.SessionID != "") && (!validWorkboardID(m.TaskID) || !validWorkboardID(m.SessionID)) {
			return invalidWorkboard("claim")
		}
	case workboard.LifecycleHeartbeat:
		if m.Actor.Type != "worker" || !validWorkboardID(m.AttemptID) || !validWorkboardID(m.ClaimID) || m.ExpectedCardRevision != 0 ||
			m.ExpectedClaimRevision < 1 || m.LeaseTTL < workboard.MinLeaseTTL || m.LeaseTTL > workboard.MaxLeaseTTL || m.PolicyDigest != "" || m.Recovery != nil ||
			m.TaskID != "" || m.SessionID != "" || m.EffectResolution != "" {
			return invalidWorkboard("heartbeat")
		}
	case workboard.LifecycleRecover:
		if m.Actor.Type != "operator" && m.Actor.Type != "system" || !validWorkboardID(m.AttemptID) || !validWorkboardID(m.ClaimID) ||
			m.ExpectedCardRevision < 1 || m.ExpectedClaimRevision < 1 || m.LeaseTTL != 0 || m.PolicyDigest != "" || m.Recovery == nil ||
			m.TaskID != "" || m.SessionID != "" || m.EffectResolution != "" || !validRecoveryIntent(*m.Recovery) || applying && !validVerifiedRecovery(m) {
			return invalidWorkboard("recovery")
		}
	case workboard.LifecycleFail:
		if m.Actor.Type != "worker" || !validWorkboardID(m.AttemptID) || !validWorkboardID(m.ClaimID) ||
			m.ExpectedCardRevision < 1 || m.ExpectedClaimRevision < 1 || m.LeaseTTL != 0 || m.PolicyDigest != "" || m.Recovery != nil ||
			m.TaskID != "" || m.SessionID != "" || m.EffectResolution != workboard.EffectFree {
			return invalidWorkboard("failure")
		}
	default:
		return invalidWorkboard("kind")
	}
	return nil
}

func validRecoveryIntent(i workboard.RecoveryIntent) bool {
	return validWorkboardID(i.StopProofID) && validDigest(i.TaskHeadDigest) && validDigest(i.ProcessProofDigest) && validDigest(i.EffectEvidenceDigest) &&
		(i.EffectResolution == workboard.EffectFree || i.EffectResolution == workboard.ResolvedNoReplay)
}

func validVerifiedRecovery(m workboard.LifecycleMutation) bool {
	p := m.Verified
	i := m.Recovery
	return p != nil && p.TaskTerminal && p.ProcessStopped && p.StopProofID == i.StopProofID && p.TaskHeadDigest == i.TaskHeadDigest &&
		p.ProcessProofDigest == i.ProcessProofDigest && p.EffectEvidenceDigest == i.EffectEvidenceDigest && p.EffectResolution == i.EffectResolution
}

func lifecycleAction(kind workboard.LifecycleKind) workboard.BoardAction {
	switch kind {
	case workboard.LifecycleClaim:
		return workboard.CardClaimAction
	case workboard.LifecycleHeartbeat:
		return workboard.ClaimHeartbeatAction
	case workboard.LifecycleRecover:
		return workboard.ClaimRecoverAction
	case workboard.LifecycleFail:
		return workboard.ClaimFailAction
	default:
		return ""
	}
}

func normalizeLifecycleWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "workboard_claims_active") || strings.Contains(message, "workboard_attempts_active") {
		return &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "claim"}
	}
	if strings.Contains(message, "unique") {
		return ErrConflict
	}
	return err
}
