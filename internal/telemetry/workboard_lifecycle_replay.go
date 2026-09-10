package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func (s *Store) ReplayLifecycleMutation(ctx context.Context, mutation workboard.LifecycleMutation) (workboard.OperationReceipt, bool, error) {
	if err := validateLifecycleMutation(mutation, false); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	want, err := workboard.LifecycleDigest(mutation)
	if err != nil || want != mutation.RequestDigest {
		return workboard.OperationReceipt{}, false, invalidWorkboard("request_digest")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	defer tx.Rollback()
	receipt, found, err := readLifecycleReplay(ctx, tx, mutation, digestBytes([]byte(mutation.IdempotencyKey)), want)
	if err != nil || !found {
		return workboard.OperationReceipt{}, found, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}

func readLifecycleReplay(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation, keyDigest, requestDigest string) (workboard.OperationReceipt, bool, error) {
	receipt, found, err := readWorkboardReceipt(ctx, tx, "board", mutation.BoardID, keyDigest, requestDigest)
	if err != nil || !found {
		return receipt, found, err
	}
	if receipt.CardID != mutation.CardID || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	if mutation.Kind == workboard.LifecycleClaim && mutation.TaskID != "" {
		if err := verifyClaimRuntimeBinding(ctx, tx, mutation); err != nil {
			return workboard.OperationReceipt{}, true, err
		}
	}
	return receipt, true, nil
}

func verifyClaimRuntimeBinding(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,attempt_id FROM workboard_claims
		WHERE board_id=? AND card_id=? AND owner_id=? AND task_id=? LIMIT 2`,
		mutation.BoardID, mutation.CardID, mutation.Actor.ID, mutation.TaskID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var claimID, attemptID string
	count := 0
	for rows.Next() {
		if rows.Scan(&claimID, &attemptID) != nil {
			return ErrWorkboardCorrupt
		}
		count++
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	if count != 1 {
		return ErrWorkboardCorrupt
	}
	_, claim, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, attemptID, claimID)
	if err != nil || claim.TaskID != mutation.TaskID || claim.OwnerID != mutation.Actor.ID {
		return ErrWorkboardCorrupt
	}
	attempt, _, err := readCanonicalAttemptSnapshot(ctx, tx, mutation.BoardID, mutation.CardID, attemptID)
	if err != nil || attempt.Claim == nil || attempt.ID != attemptID || attempt.BoardID != mutation.BoardID || attempt.CardID != mutation.CardID ||
		attempt.WorkerID != mutation.Actor.ID || attempt.Claim.ID != claimID || attempt.Claim.OwnerID != mutation.Actor.ID ||
		attempt.Claim.TaskID != mutation.TaskID {
		return ErrWorkboardCorrupt
	}
	if len(attempt.TaskIDs) != 1 || attempt.TaskIDs[0] != mutation.TaskID || len(attempt.SessionIDs) != 1 || attempt.SessionIDs[0] != mutation.SessionID {
		return ErrWorkboardCorrupt
	}
	var runtimeSession string
	if err = tx.QueryRowContext(ctx, `SELECT session_id FROM task_heads WHERE task_id=?`, mutation.TaskID).Scan(&runtimeSession); err != nil || runtimeSession != mutation.SessionID {
		return ErrWorkboardCorrupt
	}
	return nil
}
