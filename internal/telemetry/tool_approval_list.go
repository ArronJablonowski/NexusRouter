package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

// ListApprovals reads one bounded transaction-consistent page. Later pages use
// an exclusive lexical cursor, not a retained snapshot or an execution permit.
func (s *Store) ListApprovals(ctx context.Context, q approvals.ListOptions) (approvals.Page, error) {
	if q.Validate() != nil {
		return approvals.Page{}, approvals.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return approvals.Page{}, approvals.ErrUnavailable
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_heads WHERE task_id=?`, q.TaskID).Scan(&exists); err != nil {
		return approvals.Page{}, approvals.ErrUnavailable
	}
	// CASE bounds each projected value before it crosses the driver boundary.
	// Do not filter malformed rows away: any corrupt selected row fails the page.
	query := `SELECT
	CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,
	CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
	CASE WHEN length(CAST(tool_call_id AS BLOB)) BETWEEN 1 AND 128 THEN tool_call_id END,
	CASE WHEN length(CAST(state AS BLOB)) BETWEEN 1 AND 16 THEN state END,
	CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 65536 THEN body END
	FROM tool_approvals WHERE task_id=?`
	args := []any{q.TaskID}
	if q.AfterCallID != "" {
		query += ` AND tool_call_id>?`
		args = append(args, q.AfterCallID)
	}
	query += ` ORDER BY tool_call_id LIMIT ?`
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return approvals.Page{}, approvals.ErrUnavailable
	}
	defer rows.Close()
	page := approvals.Page{Version: approvals.Version, Query: q, Records: make([]approvals.Record, 0, q.Limit)}
	previous := q.AfterCallID
	for rows.Next() {
		var id, task, call, state sql.NullString
		var body []byte
		if err := rows.Scan(&id, &task, &call, &state, &body); err != nil {
			return approvals.Page{}, approvals.ErrUnavailable
		}
		var r approvals.Record
		if !id.Valid || !task.Valid || !call.Valid || !state.Valid || len(body) == 0 || json.Unmarshal(body, &r) != nil || r.Validate() != nil || r.Request.ID != id.String || r.Request.TaskID != task.String || task.String != q.TaskID || r.Request.ToolCallID != call.String || call.String <= previous || r.State != state.String {
			return approvals.Page{}, approvals.ErrInvalid
		}
		previous = call.String
		if len(page.Records) == q.Limit {
			page.NextAfterCallID = page.Records[len(page.Records)-1].Request.ToolCallID
		} else {
			page.Records = append(page.Records, r)
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return approvals.Page{}, approvals.ErrUnavailable
	}
	if page.Validate() != nil {
		return approvals.Page{}, approvals.ErrInvalid
	}
	if tx.Commit() != nil {
		return approvals.Page{}, approvals.ErrUnavailable
	}
	return page, nil
}
