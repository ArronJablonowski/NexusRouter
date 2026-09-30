package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/memory"
)

var _ memory.Exporter = (*Store)(nil)

// ExportMemory observes one scoped SQLite read snapshot. It includes private
// and expired current facts, but never retired identifiers or historical values.
// No fact is touched, corrected, migrated or redacted by this storage primitive.
func (s *Store) ExportMemory(ctx context.Context, scope string, capturedAt time.Time) (memory.ExportSnapshot, error) {
	zero := memory.ExportSnapshot{}
	if s == nil || s.db == nil || ctx == nil || !memory.ValidKey(scope) || !memory.ValidTime(capturedAt) {
		return zero, memory.ErrInput
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	// Materialization is bounded in SQL before a corrupt column reaches Go.
	// 512KiB safely covers maximal valid escaped fact strings plus metadata.
	rows, err := tx.QueryContext(ctx, `SELECT
	 CASE WHEN typeof(id)='text' AND length(CAST(id AS BLOB)) BETWEEN 1 AND 512 THEN id END,
	 CASE WHEN typeof(revision)='integer' AND revision>=1 THEN revision END,
	 CASE WHEN typeof(privacy)='text' AND length(CAST(privacy AS BLOB)) BETWEEN 1 AND 16 THEN privacy END,
	 CASE WHEN typeof(expires)='integer' THEN expires END,
	 CASE WHEN typeof(content)='text' AND length(CAST(content AS BLOB)) BETWEEN 1 AND 65536 THEN content END,
	 CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 524288 THEN body END
	 FROM memory_facts WHERE scope=? ORDER BY id LIMIT 1001`, scope)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	out := memory.ExportSnapshot{Version: 1, Scope: scope, CapturedAt: capturedAt.UTC(), Facts: []memory.Fact{}}
	envelope, err := json.Marshal(out)
	if err != nil {
		return zero, memory.ErrInput
	}
	budget := memory.ExportMaxBytes - len(envelope)
	last := ""
	for rows.Next() {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if len(out.Facts) >= memory.ExportMaxFacts {
			return zero, memory.ErrInput
		}
		var id, privacy, content string
		var revision, expires int64
		var body []byte
		if rows.Scan(&id, &revision, &privacy, &expires, &content, &body) != nil || len(body) == 0 {
			return zero, memory.ErrInput
		}
		var f memory.Fact
		if json.Unmarshal(body, &f) != nil || f.Validate() != nil || f.Scope != scope || f.ID != id || id <= last || f.Revision != revision || f.Privacy != privacy || f.Content != content {
			return zero, memory.ErrInput
		}
		if f.Expires.IsZero() && expires != 0 || !f.Expires.IsZero() && expires != f.Expires.UnixNano() {
			return zero, memory.ErrInput
		}
		encoded, err := json.Marshal(f)
		// Reject unknown, duplicate, aliased or noncanonical stored records.
		if err != nil || !bytes.Equal(body, encoded) {
			return zero, memory.ErrInput
		}
		budget -= len(encoded)
		if len(out.Facts) > 0 {
			budget--
		}
		if budget < 0 {
			return zero, memory.ErrInput
		}
		out.Facts = append(out.Facts, f)
		last = id
	}
	if err = rows.Err(); err != nil {
		return zero, err
	}
	if err = rows.Close(); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return out, nil
}
