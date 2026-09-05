package telemetry

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// DeprecationEvidence reads current judgments for a trailing original-attempt
// window. Corrections do not move an attempt forward in time. The diagnostic
// fails closed above 10,000 candidate attempts or 8 MiB of selected history.
func (s *Store) DeprecationEvidence(ctx context.Context, key routing.Key, window int) ([]evaluation.Record, error) {
	bad := func() ([]evaluation.Record, error) { return nil, evaluation.ErrEvidence }
	if ctx == nil || window < 1 || window > 1000 {
		return bad()
	}
	for _, value := range []string{key.Model, key.Provider, key.Domain, key.Profile} {
		if len(value) > 512 || strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return bad()
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Project bounded metadata before allocating bodies. Sorting parsed times
	// avoids RFC3339Nano lexical ordering errors at fractional-second boundaries.
	rows, err := tx.QueryContext(ctx, `SELECT
 CASE WHEN length(CAST(id AS BLOB)) BETWEEN 1 AND 128 THEN id END,
 CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
 CASE WHEN length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 128 THEN attempt_id END,
 CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 262144 AND json_valid(body) THEN
 CASE WHEN json_type(body,'$.Time')='text' AND length(CAST(json_extract(body,'$.Time') AS BLOB))<=64 THEN json_extract(body,'$.Time') END END
 FROM evaluations WHERE model=? AND provider=? AND domain=? AND profile=? LIMIT 10001`, key.Model, key.Provider, key.Domain, key.Profile)
	if err != nil {
		return nil, err
	}
	type attempt struct {
		id, task, attempt string
		at                time.Time
	}
	items := []attempt{}
	for rows.Next() {
		var id, task, identity, stamp sql.NullString
		if rows.Scan(&id, &task, &identity, &stamp) != nil || len(items) >= 10000 || !id.Valid || !task.Valid || !identity.Valid || !stamp.Valid || !sessions.ValidEventPageID(id.String) || !sessions.ValidEventPageID(task.String) || !sessions.ValidEventPageID(identity.String) {
			rows.Close()
			return bad()
		}
		at, err := time.Parse(time.RFC3339Nano, stamp.String)
		if err != nil || at.IsZero() {
			rows.Close()
			return bad()
		}
		items = append(items, attempt{id.String, task.String, identity.String, at})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].at.Equal(items[j].at) {
			return items[i].id < items[j].id
		}
		return items[i].at.After(items[j].at)
	})
	if len(items) > window {
		items = items[:window]
	}
	out := make([]evaluation.Record, 0, len(items))
	remaining := int64(8 << 20)
	for _, item := range items {
		if preflightEvaluationKey(ctx, tx, item.task, item.attempt, key, 512) != nil {
			return bad()
		}
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT length(CAST(body AS BLOB)) + COALESCE((SELECT sum(length(CAST(body AS BLOB))) FROM evaluation_revisions WHERE base_id=?),0) FROM evaluations WHERE id=?`, item.id, item.id).Scan(&size); err != nil {
			return nil, err
		}
		if size < 1 || size > remaining {
			return bad()
		}
		remaining -= size
		history, err := evaluationHistory(ctx, tx, item.task, item.attempt)
		if err != nil {
			return nil, err
		}
		if len(history) == 0 || history[0].ID != item.id || history[0].Key != key || !history[0].Time.Equal(item.at) {
			return bad()
		}
		out = append(out, history[len(history)-1])
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
