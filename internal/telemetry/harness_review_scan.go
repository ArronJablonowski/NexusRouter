package telemetry

import (
	"context"
	"errors"
)

// HarnessReviewScanRow includes a completed harness review identity, or only a
// position for other rows. Status can change, so callers must cycle from zero
// after reaching the end; a one-way cursor would miss later completions.
type HarnessReviewScanRow struct {
	Position          int64
	Operation, TaskID string
}

func (s *Store) ScanHarnessReviews(ctx context.Context, after int64, limit int) ([]HarnessReviewScanRow, error) {
	if s == nil || ctx == nil || after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid harness review scan")
	}
	rows, err := s.db.QueryContext(ctx, `WITH page AS MATERIALIZED (
 SELECT rowid AS position,id,task_id,status,body FROM review_attempts WHERE rowid>? ORDER BY rowid LIMIT ?)
 SELECT position,
 CASE WHEN status='completed' AND json_extract(body,'$.SourceKind')='harness' THEN CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id ELSE NULL END ELSE '' END,
 CASE WHEN status='completed' AND json_extract(body,'$.SourceKind')='harness' THEN CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id ELSE NULL END ELSE '' END
 FROM page ORDER BY position`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HarnessReviewScanRow
	for rows.Next() {
		var r HarnessReviewScanRow
		if err = rows.Scan(&r.Position, &r.Operation, &r.TaskID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
