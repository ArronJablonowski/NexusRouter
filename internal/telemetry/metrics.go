package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

var errMetrics = errors.New("metrics unavailable")

// Metrics counts lifecycle metadata in one read snapshot. Private bodies and
// identifiers are never selected, even when stored payloads are corrupt.
func (s *Store) Metrics(ctx context.Context) (metrics.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return metrics.Snapshot{}, errMetrics
	}
	defer tx.Rollback()
	var schema int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return metrics.Snapshot{}, errMetrics
	}
	snapshot := metrics.NewSnapshot(schema, time.Now().UTC())
	if snapshot.Validate() != nil {
		return metrics.Snapshot{}, errMetrics
	}
	for i := range snapshot.Groups {
		group := &snapshot.Groups[i]
		if !group.Available {
			continue
		}
		var query string
		switch group.Name {
		case "tasks":
			query = `SELECT CASE state WHEN 'running' THEN 0 WHEN 'completed' THEN 1 WHEN 'failed' THEN 2 WHEN 'canceled' THEN 3 ELSE -1 END,count(*) FROM task_heads GROUP BY 1`
		case "runtime_events":
			query = `SELECT CASE json_extract(body,'$.kind')
			 WHEN 'task.started' THEN 0 WHEN 'task.completed' THEN 1 WHEN 'task.failed' THEN 2 WHEN 'task.canceled' THEN 3
			 WHEN 'turn.started' THEN 4 WHEN 'turn.completed' THEN 5 WHEN 'model.delta' THEN 6
			 WHEN 'tool.started' THEN 7 WHEN 'tool.completed' THEN 8
			 WHEN 'worker.started' THEN 9 WHEN 'worker.heartbeat' THEN 10 WHEN 'worker.completed' THEN 11
			 WHEN 'route.selected' THEN 12 WHEN 'evaluation.recorded' THEN 13 WHEN 'error.recorded' THEN 14
			 WHEN 'steering.applied' THEN 15 WHEN 'context.compacted' THEN 16 ELSE -1 END,count(*) FROM events GROUP BY 1`
		case "runtime_operations":
			query = `SELECT operation,count(*) FROM (
			 SELECT 0 AS operation FROM events WHERE json_extract(body,'$.kind')='task.started' AND coalesce(json_extract(body,'$.data.retry_of_task_id'),'')<>''
			 UNION ALL SELECT 1 FROM events WHERE (json_extract(body,'$.kind')='task.started' AND json_type(body,'$.data.compaction')='object') OR json_extract(body,'$.kind')='context.compacted'
			 UNION ALL SELECT 2 FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_type(body,'$.data.skill_context')='object'
			 UNION ALL SELECT 3 FROM events WHERE json_extract(body,'$.kind')='route.selected' AND json_extract(body,'$.data.route.Explored')=1
			 UNION ALL SELECT 4 FROM events AS e,json_each(json_extract(e.body,'$.data.route.Excluded')) AS x,json_each(json_extract(x.value,'$.Reasons')) AS r WHERE json_extract(e.body,'$.kind')='route.selected' AND r.value='capacity'
			 UNION ALL SELECT 5 FROM events AS e,json_each(json_extract(e.body,'$.data.route.Excluded')) AS x,json_each(json_extract(x.value,'$.Reasons')) AS r WHERE json_extract(e.body,'$.kind')='route.selected' AND r.value='budget'
			 UNION ALL SELECT 6 FROM events AS e,json_each(json_extract(e.body,'$.data.route.Excluded')) AS x,json_each(json_extract(x.value,'$.Reasons')) AS r WHERE json_extract(e.body,'$.kind')='route.selected' AND r.value='privacy'
			 UNION ALL SELECT 7 FROM events AS e,json_each(json_extract(e.body,'$.data.route.Excluded')) AS x,json_each(json_extract(x.value,'$.Reasons')) AS r WHERE json_extract(e.body,'$.kind')='route.selected' AND r.value='health'
			) GROUP BY operation`
		case "submissions":
			query = `SELECT CASE state WHEN 'queued' THEN 0 WHEN 'running' THEN 1 WHEN 'succeeded' THEN 2 WHEN 'failed' THEN 3 WHEN 'canceled' THEN 4 ELSE -1 END,count(*) FROM submissions GROUP BY 1`
		case "queue_age":
			if err := readQueueAge(ctx, tx, snapshot.ObservedAt, group); err != nil {
				return metrics.Snapshot{}, errMetrics
			}
			continue
		case "queue_activity":
			// These are retained cumulative facts, not sampled rates. A service
			// start requires a durable task.started binding; terminal service
			// excludes submissions canceled or rejected before any task began.
			query = `WITH started(id,state) AS (
			 SELECT DISTINCT s.id,s.state FROM events e JOIN submissions s
			 ON json_extract(e.body,'$.data.submission_id')=s.id
			 WHERE json_extract(e.body,'$.kind')='task.started' AND json_type(e.body,'$.data.submission_id')='text'
			)
			SELECT 0,count(*) FROM submissions
			UNION ALL SELECT 1,count(*) FROM started
			UNION ALL SELECT 2,count(*) FROM started WHERE state IN ('succeeded','failed','canceled')`
		case "reviews":
			query = `SELECT CASE status WHEN 'started' THEN 0 WHEN 'completed' THEN 1 WHEN 'failed' THEN 2 ELSE -1 END,count(*) FROM review_attempts GROUP BY 1`
		case "evaluations":
			query = `SELECT 0,count(*) FROM evaluations`
		case "audits":
			query = `SELECT 0,count(*) FROM audit_records`
		case "audit_outcomes":
			query = `SELECT CASE json_extract(body,'$.Audit.verdict') WHEN 'accept' THEN 0 WHEN 'reject' THEN 1 WHEN 'abstain' THEN 2 ELSE -1 END,count(*) FROM audit_records GROUP BY 1`
		case "recoveries":
			query = `SELECT 0,count(*) FROM submission_recoveries`
		default:
			return metrics.Snapshot{}, errMetrics
		}
		rows, e := tx.QueryContext(ctx, query)
		if e != nil {
			return metrics.Snapshot{}, errMetrics
		}
		for rows.Next() {
			var index int
			var count int64
			if rows.Scan(&index, &count) != nil || index < 0 || index >= len(group.Counts) || count < 0 {
				rows.Close()
				return metrics.Snapshot{}, errMetrics
			}
			group.Counts[index].Value = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return metrics.Snapshot{}, errMetrics
		}
	}
	if schema >= 29 {
		if err := readTaskDuration(ctx, tx, &snapshot); err != nil {
			return metrics.Snapshot{}, errMetrics
		}
	}
	if schema >= 28 {
		if err := readOperationDuration(ctx, tx, &snapshot); err != nil {
			return metrics.Snapshot{}, errMetrics
		}
	}
	if schema >= 30 {
		usage, usageErr := usageTotals(ctx, tx, accounting.Scope{})
		if usageErr != nil || usage.Validate() != nil {
			return metrics.Snapshot{}, errMetrics
		}
		snapshot.Accounting = &usage
		// usageTotals records its own observation time. Advance the enclosing
		// snapshot after that read so validation cannot observe time inversion.
		snapshot.ObservedAt = time.Now().UTC()
	}
	if snapshot.Validate() != nil {
		return metrics.Snapshot{}, errMetrics
	}
	if tx.Commit() != nil {
		return metrics.Snapshot{}, errMetrics
	}
	return snapshot, nil
}

func readQueueAge(ctx context.Context, tx *sql.Tx, observedAt time.Time, group *metrics.Group) error {
	rows, err := tx.QueryContext(ctx, "SELECT created_at FROM submissions WHERE state='queued' ORDER BY rowid LIMIT ?", submissions.MaxQueued+1)
	if err != nil {
		return errMetrics
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > submissions.MaxQueued {
			return errMetrics
		}
		var encoded string
		if rows.Scan(&encoded) != nil || len(encoded) > 64 {
			return errMetrics
		}
		created, parseErr := time.Parse(time.RFC3339Nano, encoded)
		index := 7
		if parseErr == nil && !created.After(observedAt) {
			age := observedAt.Sub(created)
			switch {
			case age < time.Second:
				index = 0
			case age < 10*time.Second:
				index = 1
			case age < time.Minute:
				index = 2
			case age < 5*time.Minute:
				index = 3
			case age < 30*time.Minute:
				index = 4
			case age < time.Hour:
				index = 5
			default:
				index = 6
			}
		}
		group.Counts[index].Value++
	}
	if rows.Err() != nil {
		return errMetrics
	}
	return nil
}
