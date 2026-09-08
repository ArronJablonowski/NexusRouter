package telemetry

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// SweepLeaseAttentionPage isolates candidate failures so corrupt metadata cannot
// pin the daemon's cursor. Each changed projection/history pair commits together;
// this outer sweep is deliberately not an all-or-nothing page transaction.
// The cursor advances past attempted candidates even when they fail. Errors stay
// visible, and candidates are revisited after wrapping. No recovery is authorized.
func (s *Store) SweepLeaseAttentionPage(ctx context.Context, after string, now time.Time, limit int) (string, int, error) {
	bad := func() (string, int, error) { return after, 0, workers.ErrLeaseAttention }
	var cursor int64
	if after != "" {
		var err error
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil || cursor < 1 || strconv.FormatInt(cursor, 10) != after {
			return bad()
		}
	}
	now = now.UTC()
	if ctx == nil || limit < 1 || limit > 100 || now.Year() < 1970 || now.Year() > 2260 {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ids, err := s.attentionSweepCandidates(ctx, cursor, now, limit)
	if err != nil {
		return bad()
	}
	changed := 0
	next := after
	var failure error
	for _, id := range ids {
		if ctx.Err() != nil {
			return next, changed, workers.ErrLeaseAttention
		}
		before := ""
		if id > 1 {
			before = strconv.FormatInt(id-1, 10)
		}
		_, n, err := s.observeLeaseAttentionPage(ctx, before, now, 1, id)
		next = strconv.FormatInt(id, 10)
		if err != nil {
			failure = workers.ErrLeaseAttention
		} else {
			changed += n
		}
	}
	if ctx.Err() != nil {
		return next, changed, workers.ErrLeaseAttention
	}
	if len(ids) < limit {
		next = ""
	}
	return next, changed, failure
}

// Selection uses only row IDs, then releases the read snapshot before any write.
// An ID is a traversal position, not a lease identity or ownership capability.
func (s *Store) attentionSweepCandidates(ctx context.Context, after int64, now time.Time, limit int) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, workers.ErrLeaseAttention
	}
	defer tx.Rollback()
	var schema int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema) != nil || (schema < 26 || schema > 31) {
		return nil, workers.ErrLeaseAttention
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.rowid FROM resource_leases l
WHERE l.rowid>? AND ((l.released=0 AND l.expires<=?) OR EXISTS(SELECT 1 FROM lease_attention a WHERE a.lease_token=l.token))
ORDER BY l.rowid LIMIT ?`, after, now.UnixNano(), limit)
	if err != nil {
		return nil, workers.ErrLeaseAttention
	}
	defer rows.Close()
	ids := []int64{}
	previous := after
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil || id <= previous {
			return nil, workers.ErrLeaseAttention
		}
		ids = append(ids, id)
		previous = id
	}
	if rows.Err() != nil || rows.Close() != nil || tx.Commit() != nil {
		return nil, workers.ErrLeaseAttention
	}
	return ids, nil
}
