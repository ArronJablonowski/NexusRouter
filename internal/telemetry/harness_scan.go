package telemetry

import (
	"context"
	"errors"
)

// HarnessScanRow is a bounded journal scan position. TaskID is set only for a
// completion carrying harness evidence; callers must validate its full journal.
type HarnessScanRow struct {
	Position int64
	TaskID   string
}

// ScanHarnessCompletions scans at most limit events, including unrelated events.
// Filtering after LIMIT prevents a sparse journal from causing an unbounded scan.
func (s *Store) ScanHarnessCompletions(ctx context.Context, after int64, limit int) ([]HarnessScanRow, error) {
	if s == nil || ctx == nil || after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid harness scan")
	}
	rows, err := s.db.QueryContext(ctx, `WITH page AS MATERIALIZED (
 SELECT rowid AS position,task_id,body FROM events WHERE rowid>? ORDER BY rowid LIMIT ?)
 SELECT position,CASE WHEN json_extract(body,'$.kind')='task.completed' AND json_type(body,'$.data.harness_outcome') IS NOT NULL AND json_type(body,'$.data.harness_outcome')!='null'
 THEN CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id ELSE NULL END ELSE '' END FROM page ORDER BY position`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []HarnessScanRow
	for rows.Next() {
		var row HarnessScanRow
		if err = rows.Scan(&row.Position, &row.TaskID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
