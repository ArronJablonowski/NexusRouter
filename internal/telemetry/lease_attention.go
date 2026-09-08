package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func decodeLeaseAttention(id, task, state string, body []byte) (workers.LeaseAttention, error) {
	var record workers.LeaseAttention
	if len(body) == 0 || len(body) > 4096 || json.Unmarshal(body, &record) != nil || record.Validate() != nil || record.ID != id || record.TaskID != task || record.State != state {
		return workers.LeaseAttention{}, workers.ErrLeaseAttention
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(body, canonical) {
		return workers.LeaseAttention{}, workers.ErrLeaseAttention
	}
	return record, nil
}

// ListLeaseAttention reads durable observations, not current admission authority.
// Its cursor orders opaque record IDs; updates can move records between filters.
func (s *Store) ListLeaseAttention(ctx context.Context, options workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
	zero := workers.LeaseAttentionPage{}
	if ctx == nil || options.Validate() != nil {
		return zero, workers.ErrLeaseAttention
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, workers.ErrLeaseAttention
	}
	defer tx.Rollback()
	var schema int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema) != nil || schema < 1 || schema > 31 {
		return zero, workers.ErrLeaseAttention
	}
	page := workers.LeaseAttentionPage{Version: 1, StorageSchema: schema, Available: schema >= 24, Items: []workers.LeaseAttention{}}
	if !page.Available {
		if tx.Commit() != nil {
			return zero, workers.ErrLeaseAttention
		}
		return page, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT
CASE WHEN typeof(a.id)='text' AND length(CAST(a.id AS BLOB)) BETWEEN 1 AND 128 THEN a.id END,
CASE WHEN typeof(a.task_id)='text' AND length(CAST(a.task_id AS BLOB)) BETWEEN 1 AND 128 THEN a.task_id END,
CASE WHEN typeof(a.state)='text' AND length(CAST(a.state AS BLOB)) BETWEEN 1 AND 16 THEN a.state END,
CASE WHEN length(CAST(a.body AS BLOB)) BETWEEN 1 AND 4096 THEN a.body END,
EXISTS(SELECT 1 FROM resource_leases l JOIN task_heads h ON h.task_id=l.task_id WHERE l.token=a.lease_token AND l.task_id=a.task_id)
FROM lease_attention a WHERE a.id>? AND (?='all' OR a.state=?) ORDER BY a.id LIMIT ?`, options.After, options.State, options.State, options.Limit+1)
	if err != nil {
		return zero, workers.ErrLeaseAttention
	}
	defer rows.Close()
	for rows.Next() {
		var id, task, state sql.NullString
		var body []byte
		var bound bool
		if rows.Scan(&id, &task, &state, &body, &bound) != nil || !id.Valid || !task.Valid || !state.Valid || !bound {
			return zero, workers.ErrLeaseAttention
		}
		record, err := decodeLeaseAttention(id.String, task.String, state.String, body)
		if err != nil || record.ID <= options.After {
			return zero, workers.ErrLeaseAttention
		}
		if len(page.Items) == options.Limit {
			page.HasMore = true
			page.NextCursor = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, record)
	}
	if rows.Err() != nil || rows.Close() != nil || page.Validate() != nil || tx.Commit() != nil {
		return zero, workers.ErrLeaseAttention
	}
	return page, nil
}
