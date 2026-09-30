package telemetry

import (
	"context"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// Receipt inspection is non-mutating. It checks the exact paired terminal and
// parent edge, not a complete historical audit or new execution authority.
func validateOrphanChildReceipt(ctx context.Context, q recoveryQuery, r leaseRecoveryReceipt) error {
	if r.ChildTaskID == "" && r.ChildSequence == 0 && r.ChildEventID == "" {
		return nil
	}
	if !sessions.ValidEventPageID(r.ChildTaskID) || !sessions.ValidEventPageID(r.ChildEventID) || r.ChildTaskID == r.Task || r.ChildSequence < 2 || r.ChildSequence > 10000 {
		return ErrLeaseRecovery
	}
	var head int64
	var state string
	if q.QueryRowContext(ctx, `SELECT sequence,CASE WHEN length(CAST(state AS BLOB))<=16 THEN state END FROM task_heads WHERE task_id=?`, r.ChildTaskID).Scan(&head, &state) != nil || head != r.ChildSequence || state != "failed" {
		return ErrLeaseRecovery
	}
	var start, terminal runtime.Event
	for _, item := range []struct {
		sequence int64
		event    *runtime.Event
	}{{1, &start}, {r.ChildSequence, &terminal}} {
		var body []byte
		var id string
		if q.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8388608 THEN body END FROM events WHERE task_id=? AND sequence=?`, r.ChildTaskID, item.sequence).Scan(&id, &body) != nil || json.Unmarshal(body, item.event) != nil || item.event.Validate() != nil || item.event.ID != id || item.event.TaskID != r.ChildTaskID || item.event.Sequence != item.sequence || item.event.WorkerID != "" {
			return ErrLeaseRecovery
		}
	}
	if start.Kind != runtime.TaskStarted || start.Data.ParentTaskID != r.Task || terminal.ID != r.ChildEventID || terminal.Kind != runtime.TaskFailed || (terminal.Data.Code != "interrupted_model" && terminal.Data.Code != "interrupted_read_only_model" && terminal.Data.Code != "interrupted_read_only_tool") || !terminal.Time.Equal(r.Time) || terminal.SessionID != start.SessionID {
		return ErrLeaseRecovery
	}
	if terminal.Data.Code == "interrupted_read_only_tool" {
		return validateOrphanToolReceipt(ctx, q, r, terminal)
	}
	return nil
}
