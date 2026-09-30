package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// readSnapshotReassignment loads only lineage whose successor is the requested
// attempt. It validates the canonical record, both exact claim identities, the
// immediate attempt ordinal, and the immutable recovery before exposing it.
func readSnapshotReassignment(ctx context.Context, tx *sql.Tx, successor workboard.AttemptSnapshot) (workboard.ReassignmentRecord, bool, error) {
	var indexed workboard.ReassignmentRecord
	var created int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT recovery_id,board_id,card_id,predecessor_attempt_id,predecessor_claim_id,
		successor_attempt_id,successor_claim_id,created_at,body FROM workboard_reassignments
		WHERE board_id=? AND card_id=? AND successor_attempt_id=?`, successor.BoardID, successor.CardID, successor.ID).
		Scan(&indexed.RecoveryID, &indexed.BoardID, &indexed.CardID, &indexed.PredecessorAttemptID, &indexed.PredecessorClaimID,
			&indexed.SuccessorAttemptID, &indexed.SuccessorClaimID, &created, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.ReassignmentRecord{}, false, nil
	}
	if err != nil {
		return workboard.ReassignmentRecord{}, false, err
	}
	indexed.Version, indexed.CreatedAt = 1, time.Unix(0, created).UTC()
	var canonical workboard.ReassignmentRecord
	if strictJSON(body, &canonical) != nil || canonical.Validate() != nil || !reflect.DeepEqual(canonical, indexed) || successor.Claim == nil ||
		canonical.SuccessorAttemptID != successor.ID || canonical.SuccessorClaimID != successor.Claim.ID ||
		canonical.BoardID != successor.BoardID || canonical.CardID != successor.CardID || successor.Ordinal < 2 ||
		!canonical.CreatedAt.Equal(successor.StartedAt) {
		return workboard.ReassignmentRecord{}, false, ErrWorkboardCorrupt
	}
	if err = validateSnapshotReassignmentRecovery(ctx, tx, canonical, successor); err != nil {
		return workboard.ReassignmentRecord{}, false, err
	}
	return canonical, true, nil
}

func validateSnapshotReassignmentRecovery(ctx context.Context, tx *sql.Tx, link workboard.ReassignmentRecord, successor workboard.AttemptSnapshot) error {
	var predecessorOrdinal int
	var predecessorState string
	if err := tx.QueryRowContext(ctx, `SELECT ordinal,state FROM workboard_attempts
		WHERE board_id=? AND card_id=? AND id=?`, link.BoardID, link.CardID, link.PredecessorAttemptID).
		Scan(&predecessorOrdinal, &predecessorState); err != nil {
		return reassignmentReadError(err)
	}
	if predecessorOrdinal+1 != successor.Ordinal || predecessorState != "failed" {
		return ErrWorkboardCorrupt
	}
	predecessorLease, predecessorClaim, err := readLifecycleClaim(ctx, tx, link.BoardID, link.CardID,
		link.PredecessorAttemptID, link.PredecessorClaimID)
	if err != nil {
		return reassignmentReadError(err)
	}
	if predecessorLease.State != workboard.LeaseReleased || predecessorClaim.ReleasedAt == nil ||
		predecessorClaim.ReleasedAt.After(link.CreatedAt) {
		return ErrWorkboardCorrupt
	}
	recovery, err := readCanonicalReassignmentRecovery(ctx, tx, link.RecoveryID)
	if err != nil {
		return err
	}
	if recovery.BoardID != link.BoardID || recovery.CardID != link.CardID || recovery.AttemptID != link.PredecessorAttemptID ||
		recovery.OldClaimID != link.PredecessorClaimID || recovery.OldClaimRevision != predecessorClaim.Revision ||
		recovery.ResultingState != workboard.Ready || recovery.RecoveredAt.After(link.CreatedAt) {
		return ErrWorkboardCorrupt
	}
	return nil
}

func readCanonicalReassignmentRecovery(ctx context.Context, tx *sql.Tx, recoveryID string) (workboard.RecoveryRecord, error) {
	var indexed workboard.RecoveryRecord
	var recovered int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,proof_id,board_id,card_id,attempt_id,old_claim_id,old_claim_revision,
		card_revision,first_sequence,last_sequence,recovered_at,body FROM workboard_recoveries WHERE id=?`, recoveryID).
		Scan(&indexed.ID, &indexed.StopProofID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.OldClaimID,
			&indexed.OldClaimRevision, &indexed.CardRevision, &indexed.FirstSequence, &indexed.LastSequence, &recovered, &body)
	if err != nil {
		return workboard.RecoveryRecord{}, reassignmentReadError(err)
	}
	indexed.Version, indexed.RecoveredAt = 1, time.Unix(0, recovered).UTC()
	var canonical workboard.RecoveryRecord
	if strictJSON(body, &canonical) != nil || canonical.Validate() != nil {
		return workboard.RecoveryRecord{}, ErrWorkboardCorrupt
	}
	// Evidence digests, effect resolution, and resulting state are canonical
	// body fields; all remaining fields are normalized and compared exactly.
	indexed.TaskHeadDigest = canonical.TaskHeadDigest
	indexed.ProcessProofDigest = canonical.ProcessProofDigest
	indexed.EffectEvidenceDigest = canonical.EffectEvidenceDigest
	indexed.EffectResolution = canonical.EffectResolution
	indexed.ResultingState = canonical.ResultingState
	if !reflect.DeepEqual(canonical, indexed) {
		return workboard.RecoveryRecord{}, ErrWorkboardCorrupt
	}
	if err = validateRecoveryProofRecord(ctx, tx, canonical); err != nil {
		return workboard.RecoveryRecord{}, err
	}
	return canonical, nil
}

func reassignmentReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWorkboardCorrupt
	}
	return err
}
