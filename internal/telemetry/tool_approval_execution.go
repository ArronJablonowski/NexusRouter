package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ApprovalExecutionStatus observes journal and writer state in one read-only
// transaction. Expiry does not authorize takeover, retry, or lease release.
func (s *Store) ApprovalExecutionStatus(ctx context.Context, task, id string, now time.Time) (approvals.ExecutionStatus, error) {
	zero := approvals.ExecutionStatus{}
	_, offset := now.Zone()
	if !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(id) || now.Year() < 1970 || now.Year() >= 2261 || offset != 0 {
		return zero, approvals.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, approvals.ErrUnavailable
	}
	defer tx.Rollback()
	r, err := readApproval(ctx, tx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = approvals.ErrUnavailable
		}
		return zero, err
	}
	if r.Request.TaskID != task {
		return zero, approvals.ErrConflict
	}
	snapshot, err := taskSnapshot(ctx, tx, task)
	if err != nil {
		if ctx.Err() != nil {
			return zero, approvals.ErrUnavailable
		}
		return zero, approvals.ErrInvalid
	}
	out := approvals.ExecutionStatus{Version: approvals.Version, Approval: r, TaskState: snapshot.State, Sequence: snapshot.Sequence, ObservedAt: now, ScopeWriterState: "none"}
	if pending, ok := snapshot.Pending[r.Request.ToolCallID]; ok {
		if !pending.Dispatched || pending.TurnID != r.Request.TurnID || pending.Call.Name != r.Request.ToolName {
			return zero, approvals.ErrInvalid
		}
		out.CallState = "open"
	} else {
		if len(snapshot.Messages) != len(snapshot.MessageSequences) {
			return zero, approvals.ErrInvalid
		}
		var sequence int64
		for i, message := range snapshot.Messages {
			if message.Role == "tool" && message.ToolCallID == r.Request.ToolCallID {
				if sequence != 0 {
					return zero, approvals.ErrInvalid
				}
				sequence = snapshot.MessageSequences[i]
			}
		}
		if sequence < 2 || sequence > snapshot.Sequence {
			return zero, approvals.ErrInvalid
		}
		var eventID sql.NullString
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8388608 THEN body END FROM events WHERE task_id=? AND sequence=?`, task, sequence).Scan(&eventID, &body); err != nil {
			return zero, approvals.ErrUnavailable
		}
		var event runtime.Event
		if !eventID.Valid || len(body) == 0 || json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != eventID.String || event.TaskID != task || event.SessionID != snapshot.SessionID || event.Sequence != sequence || event.Kind != runtime.ToolCompleted || event.TurnID != r.Request.TurnID || event.Data.ToolCallID != r.Request.ToolCallID || event.Data.ToolName != r.Request.ToolName {
			return zero, approvals.ErrInvalid
		}
		out.CallState = "completed"
		out.RecordedEffect = string(event.Data.Effect)
	}
	out.ScopeWriterState, err = approvalScopeWriter(ctx, tx, r.Request.Scope, now)
	if err != nil {
		return zero, err
	}
	if out.Validate() != nil {
		return zero, approvals.ErrInvalid
	}
	if tx.Commit() != nil {
		return zero, approvals.ErrUnavailable
	}
	return out, nil
}

func approvalScopeWriter(ctx context.Context, tx *sql.Tx, scope string, now time.Time) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
	CASE WHEN length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END,
	CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
	CASE WHEN length(CAST(owner AS BLOB)) BETWEEN 1 AND 512 THEN owner END,
	CASE WHEN length(CAST(scope AS BLOB)) BETWEEN 1 AND 512 THEN scope END,
	CASE WHEN typeof(expires)='integer' THEN expires END
	FROM resource_leases WHERE scope=? AND released=0 AND writer=1 LIMIT 2`, scope)
	if err != nil {
		return "", approvals.ErrUnavailable
	}
	defer rows.Close()
	state := "none"
	for rows.Next() {
		var token, task, owner, storedScope sql.NullString
		var expires sql.NullInt64
		if rows.Scan(&token, &task, &owner, &storedScope, &expires) != nil {
			return "", approvals.ErrUnavailable
		}
		if state != "none" || !token.Valid || !task.Valid || !sessions.ValidEventPageID(task.String) || !owner.Valid || !utf8.ValidString(token.String) || !utf8.ValidString(owner.String) || !storedScope.Valid || storedScope.String != scope || !expires.Valid || expires.Int64 < 0 || time.Unix(0, expires.Int64).UTC().Year() >= 2261 {
			return "", approvals.ErrInvalid
		}
		state = "expired"
		if expires.Int64 > now.UnixNano() {
			state = "live"
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return "", approvals.ErrUnavailable
	}
	return state, nil
}
