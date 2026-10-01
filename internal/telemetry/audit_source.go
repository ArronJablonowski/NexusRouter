package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func validateAuditSource(ctx context.Context, tx *sql.Tx, task, id, kind string) error {
	if kind == "harness" {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM task_heads WHERE task_id=?", task).Scan(&state); err != nil {
			return err
		}
		if state != "completed" {
			return evaluation.ErrAudit
		}
		rows, err := tx.QueryContext(ctx, "SELECT CASE WHEN length(body)<=4194304 THEN body END FROM events WHERE task_id=? ORDER BY sequence LIMIT 3", task)
		if err != nil {
			return err
		}
		defer rows.Close()
		var events []runtime.Event
		for rows.Next() {
			var body []byte
			var event runtime.Event
			if rows.Scan(&body) != nil || json.Unmarshal(body, &event) != nil {
				return evaluation.ErrAudit
			}
			events = append(events, event)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		outcome, err := runtime.ValidateHarnessOutcome(events, task)
		if err != nil {
			return evaluation.ErrAudit
		}
		digest, _ := outcome.Digest()
		if digest != id {
			return evaluation.ErrAudit
		}
		return nil
	}
	if kind != "" {
		return evaluation.ErrAudit
	}
	var started, ended, terminal int
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.attempt_id')=? AND json_extract(body,'$.kind')='turn.started'),
 (SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.attempt_id')=? AND json_extract(body,'$.kind')='turn.completed'),
 (SELECT count(*) FROM task_heads WHERE task_id=? AND state IN ('completed','failed'))`, task, id, task, id, task).Scan(&started, &ended, &terminal)
	if err != nil {
		return err
	}
	if started != 1 || ended != 1 || terminal != 1 {
		return evaluation.ErrAudit
	}
	return nil
}
