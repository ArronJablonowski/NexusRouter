package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
)

// Timing metrics select fixed projection fields, never journal bodies or task
// identifiers. Head/event joins validate terminal bindings inside the same read
// transaction as lifecycle counts. Historical uninstrumented tasks are explicit
// missing-start observations, not zero-duration samples.
func readTaskDuration(ctx context.Context, tx *sql.Tx, snapshot *metrics.Snapshot) error {
	if snapshot.TaskDuration == nil {
		return errMetrics
	}
	var epoch string
	var version, rows int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM task_timing_metadata`).Scan(&rows) != nil || rows != 1 {
		return errMetrics
	}
	if tx.QueryRowContext(ctx, `SELECT version, CASE WHEN length(started_at) BETWEEN 1 AND 40 THEN started_at END FROM task_timing_metadata WHERE singleton=1`).Scan(&version, &epoch) != nil || version != 1 {
		return errMetrics
	}
	at, err := time.Parse(time.RFC3339Nano, epoch)
	if err != nil || epoch != at.UTC().Format(time.RFC3339Nano) {
		return errMetrics
	}
	snapshot.TaskDuration.StartedAt = at
	if err := validateTimingStarts(ctx, tx); err != nil {
		return errMetrics
	}
	var inconsistent bool
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_timings t LEFT JOIN task_heads h ON h.task_id=t.task_id WHERE h.task_id IS NULL OR h.state<>t.state OR (t.state='running' AND (t.reason<>'pending' OR t.started_at IS NULL OR t.terminal_event_id IS NOT NULL OR t.terminal_sequence IS NOT NULL OR t.duration_ns IS NOT NULL)))`).Scan(&inconsistent) != nil || inconsistent {
		return errMetrics
	}
	bounds := metrics.TaskDurationBounds()
	var bucket strings.Builder
	bucket.WriteString("CASE")
	for i, bound := range bounds {
		fmt.Fprintf(&bucket, " WHEN t.duration_ns<=%d THEN %d", int64(bound), i)
	}
	fmt.Fprintf(&bucket, " ELSE %d END", len(bounds))
	// -1 is invalid projection metadata and can never become an exported label.
	query := `SELECT CASE h.state WHEN 'completed' THEN 0 WHEN 'failed' THEN 1 WHEN 'canceled' THEN 2 ELSE -1 END,
 CASE WHEN t.task_id IS NULL THEN 11
 WHEN t.state<>h.state OR t.terminal_sequence IS NULL OR typeof(t.terminal_sequence)<>'integer' OR t.terminal_sequence<>h.sequence OR t.terminal_event_id IS NULL OR e.id IS NULL OR e.id<>t.terminal_event_id THEN -1
 WHEN t.reason='observed' AND t.started_at IS NOT NULL AND typeof(t.duration_ns)='integer' AND t.duration_ns>=0 THEN ` + bucket.String() + `
 WHEN t.reason='missing_start' AND t.started_at IS NULL AND t.duration_ns IS NULL THEN 11
 WHEN t.reason='invalid_time' AND t.started_at IS NOT NULL AND t.duration_ns IS NULL THEN 12
 ELSE -1 END,
 count(*),sum(CASE WHEN t.reason='observed' THEN CAST(t.duration_ns AS REAL)/1000000000.0 ELSE 0.0 END)
 FROM task_heads h LEFT JOIN task_timings t ON t.task_id=h.task_id
 LEFT JOIN events e ON e.task_id=h.task_id AND e.sequence=h.sequence
 WHERE h.state<>'running' GROUP BY 1,2`
	result, err := tx.QueryContext(ctx, query)
	if err != nil {
		return errMetrics
	}
	defer result.Close()
	for result.Next() {
		var state, bucket int
		var count int64
		var sum float64
		if result.Scan(&state, &bucket, &count, &sum) != nil || state < 0 || state >= 3 || bucket < 0 || bucket > 12 || count < 0 || math.IsNaN(sum) || math.IsInf(sum, 0) || sum < 0 {
			return errMetrics
		}
		group := &snapshot.TaskDuration.Groups[state]
		if bucket <= len(bounds) {
			if group.Count > math.MaxInt64-count {
				return errMetrics
			}
			group.Count += count
			group.BucketCounts[bucket] = count
			group.SumSeconds += sum
		} else {
			group.Unavailable[bucket-len(bounds)-1].Value = count
		}
	}
	return result.Err()
}

func validateTimingStarts(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(started_at AS BLOB)) BETWEEN 1 AND 40 THEN started_at END FROM task_timings WHERE started_at IS NOT NULL`)
	if err != nil {
		return errMetrics
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if rows.Scan(&value) != nil {
			return errMetrics
		}
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || at.IsZero() || value != at.UTC().Format(time.RFC3339Nano) {
			return errMetrics
		}
	}
	return rows.Err()
}
