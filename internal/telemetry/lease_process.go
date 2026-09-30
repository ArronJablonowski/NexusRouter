package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
)

func registerLeaseProcess(ctx context.Context, tx *sql.Tx, ref processguard.Reference) error {
	if ref.Validate() != nil {
		return ErrLeaseLost
	}
	body, err := json.Marshal(ref)
	if err != nil || len(body) > 8192 {
		return ErrLeaseLost
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO lease_processes(id,body) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, ref.ID, body); err != nil {
		return ErrLeaseLost
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8192 THEN body END FROM lease_processes WHERE id=?`, ref.ID).Scan(&stored); err != nil || !bytes.Equal(stored, body) {
		return ErrLeaseLost
	}
	return nil
}

// Legacy NULL identities remain unknown; this never backfills or probes them.
// Bound mutations require the current live process and exact private metadata.
func leaseProcessGate(ctx context.Context, tx *sql.Tx, token, owner string) error {
	var id sql.NullString
	var legacy bool
	if err := tx.QueryRowContext(ctx, `SELECT process_id IS NULL,CASE WHEN typeof(process_id)='text' AND length(CAST(process_id AS BLOB)) BETWEEN 1 AND 128 THEN process_id END FROM resource_leases WHERE token=? AND owner=?`, token, owner).Scan(&legacy, &id); err != nil {
		return ErrLeaseLost
	}
	if legacy {
		return nil
	}
	if !id.Valid || len(id.String) > 128 || id.String == "" {
		return ErrLeaseLost
	}
	ref, err := processguard.Current(ctx)
	if err != nil || ref.Validate() != nil || ref.ID != id.String {
		return ErrLeaseLost
	}
	body, err := json.Marshal(ref)
	if err != nil || len(body) > 8192 {
		return ErrLeaseLost
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8192 THEN body END FROM lease_processes WHERE id=?`, id.String).Scan(&stored); err != nil || !bytes.Equal(body, stored) {
		return ErrLeaseLost
	}
	return nil
}
