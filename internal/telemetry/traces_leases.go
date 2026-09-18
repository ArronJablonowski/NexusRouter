package telemetry

import (
	"context"
	"database/sql"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/traces"
)

// readTaskLeaseTrace exports fixed state classes only. Tokens, owners, scopes,
// counts and expiry instants remain private. Live/expired reflects the coherent
// snapshot time; observing either state grants no release or retry authority.
func readTaskLeaseTrace(ctx context.Context, tx *sql.Tx, task string, at, observedAt time.Time) ([]traces.Span, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
	CASE WHEN typeof(token)='text' AND length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END,
	CASE WHEN typeof(owner)='text' AND length(CAST(owner AS BLOB)) BETWEEN 1 AND 512 THEN owner END,
	CASE WHEN typeof(scope)='text' AND length(CAST(scope AS BLOB)) BETWEEN 1 AND 512 THEN scope END,
	CASE WHEN typeof(expires)='integer' THEN expires END,
	CASE WHEN typeof(writer)='integer' THEN writer END,
	CASE WHEN typeof(released)='integer' THEN released END
	FROM resource_leases WHERE task_id=? LIMIT 1001`, task)
	if err != nil {
		return nil, errTraces
	}
	defer rows.Close()
	present := map[string]bool{}
	count := 0
	for rows.Next() {
		count++
		var token, owner, scope sql.NullString
		var expires, writer, released sql.NullInt64
		if count > 1000 || rows.Scan(&token, &owner, &scope, &expires, &writer, &released) != nil ||
			!leaseObservationIdentity(token) || !leaseObservationIdentity(owner) || !leaseObservationIdentity(scope) ||
			!expires.Valid || expires.Int64 < 0 || time.Unix(0, expires.Int64).UTC().Year() >= 2261 ||
			!writer.Valid || writer.Int64 < 0 || writer.Int64 > 1 || !released.Valid || released.Int64 < 0 || released.Int64 > 1 {
			return nil, errTraces
		}
		kind := "reader_"
		if writer.Int64 == 1 {
			kind = "writer_"
		}
		switch {
		case released.Int64 == 1:
			kind += "released"
		case expires.Int64 > observedAt.UnixNano():
			kind += "live"
		default:
			kind += "expired"
		}
		present[kind] = true
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, errTraces
	}
	outcomes := []string{"reader_live", "reader_expired", "reader_released", "writer_live", "writer_expired", "writer_released"}
	out := make([]traces.Span, 0, len(outcomes))
	for _, outcome := range outcomes {
		if present[outcome] {
			out = append(out, traceInstant("resource_lease", outcome, at))
		}
	}
	return out, nil
}
