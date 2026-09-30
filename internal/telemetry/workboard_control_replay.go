package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func (s *Store) ReplayControlMutation(ctx context.Context, mutation workboard.ControlMutation) (workboard.OperationReceipt, bool, error) {
	if err := validateControlMutation(mutation, false); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	want, err := workboard.ControlDigest(mutation)
	if err != nil || want != mutation.RequestDigest {
		return workboard.OperationReceipt{}, false, invalidWorkboard("request_digest")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	defer tx.Rollback()
	receipt, found, err := readWorkboardReceipt(ctx, tx, "board", mutation.BoardID, digestBytes([]byte(mutation.IdempotencyKey)), want)
	if err != nil || !found {
		return workboard.OperationReceipt{}, found, err
	}
	wantsClaim := mutation.Kind == workboard.ControlPauseAck || mutation.Kind == workboard.ControlResumeAck ||
		mutation.Kind == workboard.ControlBlock || mutation.Kind == workboard.ControlUnblock || mutation.Kind == workboard.ControlCancelFinalize
	if receipt.CardID != mutation.CardID || receipt.CardRevision == nil || wantsClaim != (receipt.ClaimRevision != nil) {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	if mutation.Kind == workboard.ControlCancelFinalize {
		if err = validateExecutionSettlementReplay(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID, runtime.TaskCanceled, false); err != nil {
			return workboard.OperationReceipt{}, true, err
		}
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}
