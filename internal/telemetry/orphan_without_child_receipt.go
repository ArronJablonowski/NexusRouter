package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// The new no-child receipt rederives its strict worker prefix and terminal.
// Legacy child receipts retain their existing narrower paired-event checks.
func validateOrphanWithoutChildReceipt(ctx context.Context, q recoveryQuery, r leaseRecoveryReceipt) error {
	reader, ok := q.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return ErrLeaseRecovery
	}
	var count, size int64
	if q.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(length(CAST(body AS BLOB))),0) FROM events WHERE task_id=?`, r.Task).Scan(&count, &size) != nil || count != r.Sequence || count < 2 || count > 10000 || size < 1 || size > 8<<20 {
		return ErrLeaseRecovery
	}
	rows, err := reader.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,sequence,CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8388608 THEN body END FROM events WHERE task_id=? ORDER BY sequence LIMIT 10001`, r.Task)
	if err != nil {
		return ErrLeaseRecovery
	}
	defer rows.Close()
	history := make([]runtime.Event, 0, count)
	var terminalBody []byte
	var actualBytes int64
	for rows.Next() {
		if int64(len(history)) >= count {
			return ErrLeaseRecovery
		}
		var id string
		var sequence int64
		var body []byte
		var event runtime.Event
		if rows.Scan(&id, &sequence, &body) != nil || sequence != int64(len(history))+1 || json.Unmarshal(body, &event) != nil || event.ID != id || event.TaskID != r.Task || event.Sequence != sequence {
			return ErrLeaseRecovery
		}
		actualBytes += int64(len(body))
		if len(body) == 0 || actualBytes > 8<<20 {
			return ErrLeaseRecovery
		}
		canonical, encodeErr := event.Encode()
		if encodeErr != nil || !bytes.Equal(body, canonical) {
			return ErrLeaseRecovery
		}
		history = append(history, event)
		terminalBody = body
	}
	if rows.Err() != nil || rows.Close() != nil || int64(len(history)) != count {
		return ErrLeaseRecovery
	}
	plan, err := sessions.PlanInterruptedWorkerWithoutChild(history[:len(history)-1], r.Time)
	if err != nil || len(plan.Events) != 1 || plan.Events[0].ID != r.EventID {
		return ErrLeaseRecovery
	}
	want, err := plan.Events[0].Encode()
	if err != nil || !bytes.Equal(want, terminalBody) {
		return ErrLeaseRecovery
	}
	return nil
}
