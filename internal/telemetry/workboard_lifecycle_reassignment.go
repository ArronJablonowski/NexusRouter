package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// insertDerivedReassignment records recovery lineage as part of the successor
// claim transaction. The caller cannot supply any lineage identifiers: they
// are derived from the card's durable predecessor and its canonical recovery.
func insertDerivedReassignment(
	ctx context.Context,
	tx *sql.Tx,
	mutation workboard.LifecycleMutation,
	predecessorAttemptID, successorAttemptID, successorClaimID string,
) (int, error) {
	if predecessorAttemptID == "" {
		return 0, nil
	}
	recovery, found, err := readImmediateRecovery(ctx, tx, mutation.BoardID, mutation.CardID, predecessorAttemptID)
	if err != nil || !found {
		return 0, err
	}
	if recovery.CardRevision > mutation.ExpectedCardRevision {
		return 0, ErrWorkboardCorrupt
	}
	predecessor, _, err := readCanonicalAttemptSnapshot(ctx, tx, mutation.BoardID, mutation.CardID, predecessorAttemptID)
	if err != nil || predecessor.State != "failed" || predecessor.Claim == nil || predecessor.Claim.ID != recovery.OldClaimID ||
		predecessor.Claim.State != string(workboard.LeaseReleased) || predecessor.Ordinal < 1 {
		return 0, ErrWorkboardCorrupt
	}
	record := workboard.ReassignmentRecord{
		Version:              1,
		RecoveryID:           recovery.ID,
		BoardID:              mutation.BoardID,
		CardID:               mutation.CardID,
		PredecessorAttemptID: predecessorAttemptID,
		PredecessorClaimID:   recovery.OldClaimID,
		SuccessorAttemptID:   successorAttemptID,
		SuccessorClaimID:     successorClaimID,
		CreatedAt:            mutation.Now,
	}
	if record.Validate() != nil {
		return 0, ErrWorkboardCorrupt
	}
	body, err := encodeLifecycle(record, 16<<10)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_reassignments(
		recovery_id,board_id,card_id,predecessor_attempt_id,predecessor_claim_id,successor_attempt_id,successor_claim_id,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?)`, record.RecoveryID, record.BoardID, record.CardID, record.PredecessorAttemptID,
		record.PredecessorClaimID, record.SuccessorAttemptID, record.SuccessorClaimID, record.CreatedAt.UnixNano(), body)
	if err != nil {
		return 0, normalizeLifecycleWriteError(err)
	}
	return len(body), nil
}

func readImmediateRecovery(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) (workboard.RecoveryRecord, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,proof_id,board_id,card_id,attempt_id,old_claim_id,old_claim_revision,
		card_revision,first_sequence,last_sequence,recovered_at,body FROM workboard_recoveries
		WHERE board_id=? AND card_id=? AND attempt_id=? ORDER BY recovered_at,id LIMIT 2`, boardID, cardID, attemptID)
	if err != nil {
		return workboard.RecoveryRecord{}, false, err
	}
	defer rows.Close()
	var result workboard.RecoveryRecord
	count := 0
	for rows.Next() {
		var indexed workboard.RecoveryRecord
		var recoveredAt int64
		var body []byte
		if err = rows.Scan(&indexed.ID, &indexed.StopProofID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID,
			&indexed.OldClaimID, &indexed.OldClaimRevision, &indexed.CardRevision, &indexed.FirstSequence,
			&indexed.LastSequence, &recoveredAt, &body); err != nil {
			return workboard.RecoveryRecord{}, false, err
		}
		indexed.Version = 1
		indexed.RecoveredAt = unixTime(recoveredAt)
		var canonical workboard.RecoveryRecord
		if strictJSON(body, &canonical) != nil || canonical.Validate() != nil ||
			canonical.ID != indexed.ID || canonical.StopProofID != indexed.StopProofID || canonical.BoardID != indexed.BoardID ||
			canonical.CardID != indexed.CardID || canonical.AttemptID != indexed.AttemptID || canonical.OldClaimID != indexed.OldClaimID ||
			canonical.OldClaimRevision != indexed.OldClaimRevision || canonical.CardRevision != indexed.CardRevision ||
			canonical.FirstSequence != indexed.FirstSequence || canonical.LastSequence != indexed.LastSequence ||
			!canonical.RecoveredAt.Equal(indexed.RecoveredAt) || canonical.BoardID != boardID || canonical.CardID != cardID || canonical.AttemptID != attemptID {
			return workboard.RecoveryRecord{}, false, ErrWorkboardCorrupt
		}
		result = canonical
		count++
	}
	if err = rows.Err(); err != nil {
		return workboard.RecoveryRecord{}, false, err
	}
	if count > 1 {
		return workboard.RecoveryRecord{}, false, ErrWorkboardCorrupt
	}
	if count == 1 {
		if err = validateRecoveryProofRecord(ctx, tx, result); err != nil {
			return workboard.RecoveryRecord{}, false, err
		}
	}
	return result, count == 1, nil
}

func validateRecoveryProofRecord(ctx context.Context, tx *sql.Tx, recovery workboard.RecoveryRecord) error {
	var indexed storedRecoveryProof
	var createdAt int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,board_id,card_id,attempt_id,claim_id,task_head_digest,process_proof_digest,
		effect_evidence_digest,effect_resolution,created_at,body FROM workboard_recovery_proofs WHERE id=?`, recovery.StopProofID).
		Scan(&indexed.ID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.ClaimID, &indexed.TaskHeadDigest,
			&indexed.ProcessProofDigest, &indexed.EffectEvidenceDigest, &indexed.EffectResolution, &createdAt, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWorkboardCorrupt
	}
	if err != nil {
		return err
	}
	indexed.Version, indexed.CreatedAt = 1, unixTime(createdAt)
	var canonical storedRecoveryProof
	if strictJSON(body, &canonical) != nil || !equalRecoveryProof(indexed, canonical) ||
		!validWorkboardID(canonical.ID) || !validWorkboardID(canonical.BoardID) || !validWorkboardID(canonical.CardID) ||
		!validWorkboardID(canonical.AttemptID) || !validWorkboardID(canonical.ClaimID) || !validDigest(canonical.TaskHeadDigest) ||
		!validDigest(canonical.ProcessProofDigest) || !validDigest(canonical.EffectEvidenceDigest) ||
		canonical.EffectResolution != string(workboard.EffectFree) && canonical.EffectResolution != string(workboard.ResolvedNoReplay) ||
		canonical.CreatedAt.Location() != time.UTC || canonical.CreatedAt.Year() < 1970 || canonical.CreatedAt.Year() >= 2261 ||
		canonical.CreatedAt.After(recovery.RecoveredAt) ||
		canonical.ID != recovery.StopProofID || canonical.BoardID != recovery.BoardID || canonical.CardID != recovery.CardID ||
		canonical.AttemptID != recovery.AttemptID || canonical.ClaimID != recovery.OldClaimID ||
		canonical.TaskHeadDigest != recovery.TaskHeadDigest || canonical.ProcessProofDigest != recovery.ProcessProofDigest ||
		canonical.EffectEvidenceDigest != recovery.EffectEvidenceDigest || canonical.EffectResolution != string(recovery.EffectResolution) {
		return ErrWorkboardCorrupt
	}
	return nil
}

func equalRecoveryProof(a, b storedRecoveryProof) bool {
	return a.Version == b.Version && a.ID == b.ID && a.BoardID == b.BoardID && a.CardID == b.CardID &&
		a.AttemptID == b.AttemptID && a.ClaimID == b.ClaimID && a.TaskHeadDigest == b.TaskHeadDigest &&
		a.ProcessProofDigest == b.ProcessProofDigest && a.EffectEvidenceDigest == b.EffectEvidenceDigest &&
		a.EffectResolution == b.EffectResolution && a.CreatedAt.Equal(b.CreatedAt)
}

func unixTime(nanos int64) time.Time {
	return time.Unix(0, nanos).UTC()
}
