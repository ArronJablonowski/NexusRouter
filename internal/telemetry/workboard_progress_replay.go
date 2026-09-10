package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func (s *Store) ReplayProgressMutation(ctx context.Context, mutation workboard.ProgressMutation) (workboard.OperationReceipt, bool, error) {
	if err := validateProgressMutation(mutation); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	want, err := workboard.ProgressDigest(mutation)
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
	if receipt.CardID != mutation.CardID || receipt.CardRevision == nil ||
		(mutation.Kind == workboard.ProgressCheckpointAppend) != (receipt.ClaimRevision != nil) {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}
