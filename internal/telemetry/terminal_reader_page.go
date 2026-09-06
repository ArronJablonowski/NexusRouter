package telemetry

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// RecoverTerminalReadersPage uses a private decimal rowid cursor. Held and
// unverifiable owners are visited and skipped, so they cannot starve later rows.
// Each candidate gets bounded replay; the page shares a five-second deadline.
func (s *Store) RecoverTerminalReadersPage(ctx context.Context, after string, limit int, now time.Time) (next string, recovered int, err error) {
	now = now.UTC()
	var cursor int64
	if len(after) > 19 {
		return "", 0, ErrLeaseRecovery
	}
	if after != "" {
		var e error
		cursor, e = strconv.ParseInt(after, 10, 64)
		if e != nil || cursor < 1 || strconv.FormatInt(cursor, 10) != after {
			return "", 0, ErrLeaseRecovery
		}
	}
	if ctx == nil || limit < 1 || limit > 100 || now.Year() < 1970 || now.Year() >= 2261 {
		return "", 0, ErrLeaseRecovery
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	next = after
	rows, err := s.db.QueryContext(ctx, `SELECT l.rowid,CASE WHEN typeof(l.token)='text' AND length(CAST(l.token AS BLOB)) BETWEEN 1 AND 512 THEN l.token END FROM resource_leases l JOIN task_heads h ON h.task_id=l.task_id WHERE l.rowid>? AND l.writer=0 AND l.released=0 AND l.process_id IS NOT NULL AND h.state IN ('completed','failed','canceled') ORDER BY l.rowid LIMIT ?`, cursor, limit)
	if err != nil {
		return after, 0, ErrLeaseRecovery
	}
	type entry struct {
		id    int64
		token sql.NullString
	}
	tokens := []entry{}
	for rows.Next() {
		var token entry
		if rows.Scan(&token.id, &token.token) != nil || token.id <= cursor {
			rows.Close()
			return after, 0, ErrLeaseRecovery
		}
		tokens = append(tokens, token)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return after, 0, ErrLeaseRecovery
	}
	for _, token := range tokens {
		next = strconv.FormatInt(token.id, 10)
		if !leaseObservationIdentity(token.token) {
			return next, recovered, ErrLeaseRecovery
		}
		changed, e := s.RecoverTerminalReader(ctx, token.token.String, now)
		if e != nil {
			return next, recovered, e
		}
		if changed {
			recovered++
		}
	}
	if len(tokens) < limit {
		next = ""
	}
	return next, recovered, nil
}
