package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
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
		case "submissions":
			query = `SELECT CASE state WHEN 'queued' THEN 0 WHEN 'running' THEN 1 WHEN 'succeeded' THEN 2 WHEN 'failed' THEN 3 WHEN 'canceled' THEN 4 ELSE -1 END,count(*) FROM submissions GROUP BY 1`
		case "reviews":
			query = `SELECT CASE status WHEN 'started' THEN 0 WHEN 'completed' THEN 1 WHEN 'failed' THEN 2 ELSE -1 END,count(*) FROM review_attempts GROUP BY 1`
		case "evaluations":
			query = `SELECT 0,count(*) FROM evaluations`
		case "audits":
			query = `SELECT 0,count(*) FROM audit_records`
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
	if snapshot.Validate() != nil {
		return metrics.Snapshot{}, errMetrics
	}
	if tx.Commit() != nil {
		return metrics.Snapshot{}, errMetrics
	}
	return snapshot, nil
}
