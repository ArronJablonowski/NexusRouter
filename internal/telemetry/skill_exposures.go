package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type skillExposureExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// The index is an accelerator, never outcome or attribution authority. All
// consumers must validate the selected task's immutable journal before use.
func appendSkillExposures(ctx context.Context, db skillExposureExecutor, e runtime.Event) error {
	if e.Kind != runtime.TaskStarted {
		return nil
	}
	if e.Validate() != nil || e.Sequence != 1 {
		return sessions.ErrHistory
	}
	use := e.Data.SkillContext
	if use == nil || !use.Complete {
		return nil
	}
	if e.Data.Privacy != "" && e.Data.Privacy != "local_only" && e.Data.Privacy != "cloud_allowed" {
		return sessions.ErrHistory
	}
	for _, ref := range use.References {
		result, err := db.ExecContext(ctx, `INSERT INTO skill_exposures(task_id,scope,name,version,digest,ordinal,privacy) SELECT ?,?,?,?,?,seq,? FROM workflow_scan_tasks WHERE task_id=? AND seq>0`, e.TaskID, ref.Scope, ref.Name, ref.Version, ref.Digest, e.Data.Privacy, e.TaskID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
	}
	return nil
}

func migrateSkillExposures(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE skill_exposures(task_id TEXT NOT NULL REFERENCES task_heads(task_id),scope TEXT NOT NULL,name TEXT NOT NULL,version TEXT NOT NULL,digest TEXT NOT NULL,ordinal INTEGER NOT NULL CHECK(ordinal>0),privacy TEXT NOT NULL,PRIMARY KEY(task_id,scope,name)); CREATE INDEX skill_exposures_lookup ON skill_exposures(scope,name,version,privacy,ordinal DESC,task_id); CREATE INDEX task_heads_session ON task_heads(session_id,task_id)`); err != nil {
		return err
	}
	after := ""
	for {
		// Only bounded identity/length metadata is fetched before each body.
		rows, err := conn.QueryContext(ctx, `SELECT CASE WHEN length(CAST(e.task_id AS BLOB)) BETWEEN 1 AND 128 THEN e.task_id END,CASE WHEN length(CAST(e.id AS BLOB)) BETWEEN 1 AND 128 THEN e.id END,CASE WHEN length(CAST(h.session_id AS BLOB)) BETWEEN 1 AND 128 THEN h.session_id END,length(CAST(e.body AS BLOB)) FROM events e JOIN task_heads h ON h.task_id=e.task_id WHERE e.sequence=1 AND e.task_id>? ORDER BY e.task_id LIMIT 128`, after)
		if err != nil {
			return err
		}
		type entry struct {
			task, id, session string
			size              int64
		}
		page := []entry{}
		for rows.Next() {
			var task, id, session sql.NullString
			var size int64
			if err = rows.Scan(&task, &id, &session, &size); err != nil {
				rows.Close()
				return err
			}
			if !task.Valid || !id.Valid || !session.Valid || !sessions.ValidEventPageID(task.String) || !sessions.ValidEventPageID(id.String) || !sessions.ValidEventPageID(session.String) || task.String <= after || size < 1 || size > 8<<20 {
				rows.Close()
				return sessions.ErrHistory
			}
			page = append(page, entry{task.String, id.String, session.String, size})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range page {
			if err = ctx.Err(); err != nil {
				return err
			}
			var body []byte
			if err = conn.QueryRowContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence=1", item.task).Scan(&body); err != nil {
				return err
			}
			var e runtime.Event
			if int64(len(body)) != item.size || json.Unmarshal(body, &e) != nil || e.Validate() != nil || e.Kind != runtime.TaskStarted || e.Sequence != 1 || e.TaskID != item.task || e.ID != item.id || e.SessionID != item.session {
				return sessions.ErrHistory
			}
			if err = appendSkillExposures(ctx, conn, e); err != nil {
				return err
			}
			after = item.task
		}
		if len(page) < 128 {
			break
		}
	}
	_, err := conn.ExecContext(ctx, "PRAGMA user_version=27")
	return err
}
