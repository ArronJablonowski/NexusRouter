package telemetry

import (
	"context"
	"database/sql"
	"errors"
)

// Schema 39 adds the durable pause phase to the canonical card body while
// retaining pause_requested as the normalized version-1 compatibility bit.
// A legacy true bit proves only that pause was requested, never acknowledged.
func migrateWorkboardPausePhase(ctx context.Context, conn *sql.Conn) error {
	var tables int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_cards'`).Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		_, err := conn.ExecContext(ctx, "PRAGMA user_version=39")
		return err
	}
	var invalid int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM workboard_cards
		WHERE json_valid(body)=0 OR json_type(body,'$.pause_phase') IS NOT NULL OR
		json_type(body,'$.pause_requested') NOT IN('true','false') OR
		CAST(json_extract(body,'$.pause_requested') AS INTEGER)!=pause_requested`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("invalid legacy workboard pause state")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE workboard_cards
		SET body=json_set(body,'$.pause_phase','requested') WHERE pause_requested=1`); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM workboard_cards
		WHERE pause_requested=1 AND json_extract(body,'$.pause_phase')!='requested' OR
		pause_requested=0 AND json_type(body,'$.pause_phase') IS NOT NULL`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("workboard pause migration did not converge")
	}
	_, err := conn.ExecContext(ctx, "PRAGMA user_version=39")
	return err
}
