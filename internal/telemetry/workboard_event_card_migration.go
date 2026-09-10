package telemetry

import (
	"context"
	"database/sql"
)

// Schema 36 adds normalized card identity to immutable workboard events. The
// outer serialized BEGIN IMMEDIATE transaction makes the table rebuild atomic
// across processes and restart-safe. Existing schema-35 board events have no
// card identity to recover and are intentionally backfilled with NULL.
func migrateWorkboardEventCardIdentity(ctx context.Context, conn *sql.Conn) error {
	if err := validateWorkboardSchema36(ctx, conn); err == nil {
		_, err = conn.ExecContext(ctx, "PRAGMA user_version=36")
		return err
	}
	if err := validateWorkboardSchema(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE workboard_events RENAME TO workboard_events_v35;
		DROP INDEX workboard_events_operation;
		CREATE TABLE workboard_events(
		 id TEXT NOT NULL UNIQUE CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
		 board_id TEXT NOT NULL REFERENCES workboard_boards(id) ON DELETE CASCADE,
		 sequence INTEGER NOT NULL CHECK(sequence>0),
		 operation_id TEXT NOT NULL,
		 kind TEXT NOT NULL CHECK(length(CAST(kind AS BLOB)) BETWEEN 1 AND 128),
		 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
		 actor_type TEXT NOT NULL CHECK(actor_type IN('operator','worker','validator','model','system')),
		 card_id TEXT CHECK(length(CAST(card_id AS BLOB)) BETWEEN 1 AND 128),
		 created_at INTEGER NOT NULL CHECK(created_at>=0),
		 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
		 PRIMARY KEY(board_id,sequence),
		 FOREIGN KEY(board_id,card_id) REFERENCES workboard_cards(board_id,id),
		 FOREIGN KEY(board_id,operation_id) REFERENCES workboard_operations(board_id,operation_id) DEFERRABLE INITIALLY DEFERRED);
		INSERT INTO workboard_events(id,board_id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body)
		 SELECT id,board_id,sequence,operation_id,kind,actor_id,actor_type,NULL,created_at,body FROM workboard_events_v35;
		DROP TABLE workboard_events_v35;
		CREATE INDEX workboard_events_operation ON workboard_events(operation_id,sequence);`); err != nil {
		return err
	}
	if err := validateWorkboardSchema36(ctx, conn); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "PRAGMA user_version=36")
	return err
}
