package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func decodeAttentionTransition(id string, sequence int64, kind string, body []byte) (workers.LeaseAttentionTransition, error) {
	zero := workers.LeaseAttentionTransition{}
	var observation workers.LeaseAttention
	if len(body) == 0 || len(body) > 4096 || json.Unmarshal(body, &observation) != nil {
		return zero, workers.ErrLeaseAttention
	}
	canonical, err := json.Marshal(observation)
	transition := workers.LeaseAttentionTransition{Version: 1, Sequence: sequence, Kind: kind, Observation: observation}
	if err != nil || !bytes.Equal(body, canonical) || observation.ID != id || transition.Validate() != nil {
		return zero, workers.ErrLeaseAttention
	}
	return transition, nil
}

func readAttentionTransition(ctx context.Context, tx *sql.Tx, id string, sequence int64) (workers.LeaseAttentionTransition, error) {
	var number sql.NullInt64
	var kind sql.NullString
	var body []byte
	query := `SELECT CASE WHEN typeof(sequence)='integer' THEN sequence END,
CASE WHEN typeof(kind)='text' AND length(CAST(kind AS BLOB)) BETWEEN 1 AND 16 THEN kind END,
CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 4096 THEN body END
FROM lease_attention_history WHERE attention_id=?`
	args := []any{id}
	if sequence > 0 {
		query += " AND sequence=?"
		args = append(args, sequence)
	}
	query += " ORDER BY sequence DESC LIMIT 1"
	err := tx.QueryRowContext(ctx, query, args...).Scan(&number, &kind, &body)
	if err != nil {
		return workers.LeaseAttentionTransition{}, err
	}
	if !number.Valid || !kind.Valid {
		return workers.LeaseAttentionTransition{}, workers.ErrLeaseAttention
	}
	return decodeAttentionTransition(id, number.Int64, kind.String, body)
}

func attentionHistoryMatches(a, b workers.LeaseAttention) bool {
	x, err := json.Marshal(a)
	y, other := json.Marshal(b)
	return err == nil && other == nil && bytes.Equal(x, y)
}

// The latest projection and its history must agree before either is advanced.
func attentionHistoryHead(ctx context.Context, tx *sql.Tx, current workers.LeaseAttention) (int64, error) {
	head, err := readAttentionTransition(ctx, tx, current.ID, 0)
	if err != nil || !attentionHistoryMatches(head.Observation, current) {
		return 0, workers.ErrLeaseAttention
	}
	return head.Sequence, nil
}

// appendAttentionTransition runs inside the same writer transaction as the
// projection change. No runtime operation updates or deletes history rows.
func appendAttentionTransition(ctx context.Context, tx *sql.Tx, record workers.LeaseAttention, previous int64) error {
	if previous < 0 || previous == math.MaxInt64 || record.Validate() != nil {
		return workers.ErrLeaseAttention
	}
	body, err := json.Marshal(record)
	if err != nil || len(body) > 4096 {
		return workers.ErrLeaseAttention
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO lease_attention_history(attention_id,sequence,kind,body) VALUES(?,?,'observed',?)`, record.ID, previous+1, body)
	if err != nil {
		return workers.ErrLeaseAttention
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return workers.ErrLeaseAttention
	}
	return nil
}

// ListLeaseAttentionHistory returns a bounded, coherent read-only page. Schema24
// has no history; migration later records a baseline, not invented prior events.
func (s *Store) ListLeaseAttentionHistory(ctx context.Context, id string, options workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
	zero := workers.LeaseAttentionHistoryPage{}
	bad := func() (workers.LeaseAttentionHistoryPage, error) { return zero, workers.ErrLeaseAttention }
	if ctx == nil || !sessions.ValidEventPageID(id) || options.Validate() != nil {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return bad()
	}
	defer tx.Rollback()
	var schema int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema) != nil || schema < 1 || schema > 32 {
		return bad()
	}
	page := workers.LeaseAttentionHistoryPage{Version: 1, StorageSchema: schema, Available: schema >= 25, AttentionID: id, Items: []workers.LeaseAttentionTransition{}}
	if !page.Available {
		if tx.Commit() != nil {
			return bad()
		}
		return page, nil
	}
	var task, state sql.NullString
	var body []byte
	var bound bool
	err = tx.QueryRowContext(ctx, `SELECT
CASE WHEN typeof(a.task_id)='text' AND length(CAST(a.task_id AS BLOB)) BETWEEN 1 AND 128 THEN a.task_id END,
CASE WHEN typeof(a.state)='text' AND length(CAST(a.state AS BLOB)) BETWEEN 1 AND 16 THEN a.state END,
CASE WHEN length(CAST(a.body AS BLOB)) BETWEEN 1 AND 4096 THEN a.body END,
EXISTS(SELECT 1 FROM resource_leases l JOIN task_heads h ON h.task_id=l.task_id WHERE l.token=a.lease_token AND l.task_id=a.task_id)
FROM lease_attention a WHERE a.id=?`, id).Scan(&task, &state, &body, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, sql.ErrNoRows
	}
	if err != nil || !task.Valid || !state.Valid || !bound {
		return bad()
	}
	current, err := decodeLeaseAttention(id, task.String, state.String, body)
	if err != nil {
		return bad()
	}
	head, err := attentionHistoryHead(ctx, tx, current)
	if err != nil {
		return bad()
	}
	var previous workers.LeaseAttentionTransition
	if options.AfterSequence > 0 && options.AfterSequence <= head {
		previous, err = readAttentionTransition(ctx, tx, id, options.AfterSequence)
		if err != nil {
			return bad()
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN typeof(sequence)='integer' THEN sequence END,
CASE WHEN typeof(kind)='text' AND length(CAST(kind AS BLOB)) BETWEEN 1 AND 16 THEN kind END,
CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 4096 THEN body END
FROM lease_attention_history WHERE attention_id=? AND sequence>? ORDER BY sequence LIMIT ?`, id, options.AfterSequence, options.Limit+1)
	if err != nil {
		return bad()
	}
	defer rows.Close()
	expected := options.AfterSequence + 1
	for rows.Next() {
		var sequence sql.NullInt64
		var kind sql.NullString
		var encoded []byte
		if rows.Scan(&sequence, &kind, &encoded) != nil || !sequence.Valid || !kind.Valid || sequence.Int64 != expected {
			return bad()
		}
		item, err := decodeAttentionTransition(id, sequence.Int64, kind.String, encoded)
		if err != nil || !attentionIdentityMatches(item.Observation, current) || item.Observation.UpdatedAt.After(current.UpdatedAt) || previous.Version != 0 && !attentionFollows(previous.Observation, item.Observation) {
			return bad()
		}
		if len(page.Items) == options.Limit {
			page.HasMore = true
			page.NextSequence = page.Items[len(page.Items)-1].Sequence
			break
		}
		page.Items = append(page.Items, item)
		previous = item
		expected++
	}
	if rows.Err() != nil || rows.Close() != nil || page.Validate() != nil || tx.Commit() != nil {
		return bad()
	}
	return page, nil
}

func attentionIdentityMatches(a, b workers.LeaseAttention) bool {
	return a.ID == b.ID && a.TaskID == b.TaskID && a.Writer == b.Writer && a.FirstObserved.Equal(b.FirstObserved)
}

func attentionFollows(a, b workers.LeaseAttention) bool {
	return attentionIdentityMatches(a, b) && !b.UpdatedAt.Before(a.UpdatedAt) && (a.State != b.State || a.Reason != b.Reason || !a.LeaseExpires.Equal(b.LeaseExpires))
}
