package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// verifyWorkboardReceiptEvents binds an exact-replay response back to the
// immutable journal. The events foreign key prevents orphan events, but not an
// operation whose declared events were deleted or altered after commit.
func verifyWorkboardReceiptEvents(ctx context.Context, tx *sql.Tx, receipt workboard.OperationReceipt) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
		FROM workboard_events WHERE board_id=? AND operation_id=? ORDER BY sequence`, receipt.BoardID, receipt.OperationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		event, scanErr := scanCanonicalWorkboardEvent(rows, receipt.BoardID)
		if scanErr != nil {
			return scanErr
		}
		if count >= receipt.EventCount || event.OperationID != receipt.OperationID || event.Sequence != receipt.FirstSequence+int64(count) {
			return ErrWorkboardCorrupt
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != receipt.EventCount || count == 0 || receipt.FirstSequence+int64(count)-1 != receipt.LastSequence {
		return ErrWorkboardCorrupt
	}
	return nil
}
