package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// Reconstruct the entire synthetic suffix from its exact original-prefix marker.
// Canonical bounded raw events prevent ignored JSON fields from hiding drift.
func validateOrphanToolReceipt(ctx context.Context, q recoveryQuery, r leaseRecoveryReceipt, terminal runtime.Event) error {
	reader, ok := q.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return ErrLeaseRecovery
	}
	var count, size int64
	if q.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(length(CAST(body AS BLOB))),0) FROM events WHERE task_id=?`, r.ChildTaskID).Scan(&count, &size) != nil || count != r.ChildSequence || count < 4 || count > 10000 || size < 1 || size > 8<<20 {
		return ErrLeaseRecovery
	}
	rows, err := reader.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,sequence,CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8388608 THEN body END FROM events WHERE task_id=? ORDER BY sequence LIMIT 10001`, r.ChildTaskID)
	if err != nil {
		return ErrLeaseRecovery
	}
	defer rows.Close()
	history := make([]runtime.Event, 0, count)
	var actual int64
	prefixEnd := -1
	for rows.Next() {
		var id string
		var seq int64
		var body []byte
		var event runtime.Event
		if int64(len(history)) >= count || rows.Scan(&id, &seq, &body) != nil || len(body) == 0 {
			return ErrLeaseRecovery
		}
		actual += int64(len(body))
		if actual > 8<<20 || json.Unmarshal(body, &event) != nil || event.ID != id || event.Sequence != seq || seq != int64(len(history))+1 || event.TaskID != r.ChildTaskID {
			return ErrLeaseRecovery
		}
		canonical, err := event.Encode()
		if err != nil || !bytes.Equal(body, canonical) {
			return ErrLeaseRecovery
		}
		history = append(history, event)
		if id == terminal.CausationID {
			if prefixEnd != -1 {
				return ErrLeaseRecovery
			}
			prefixEnd = len(history)
		}
	}
	if rows.Err() != nil || rows.Close() != nil || int64(len(history)) != count || prefixEnd < 1 || prefixEnd >= len(history) {
		return ErrLeaseRecovery
	}
	plan, err := sessions.PlanInterruptedReadOnlyTools([][]runtime.Event{history[:prefixEnd]}, r.Time)
	if err != nil || len(plan.Events) != len(history)-prefixEnd {
		return ErrLeaseRecovery
	}
	want, _ := json.Marshal(plan.Events)
	got, _ := json.Marshal(history[prefixEnd:])
	if !bytes.Equal(want, got) {
		return ErrLeaseRecovery
	}
	return nil
}
