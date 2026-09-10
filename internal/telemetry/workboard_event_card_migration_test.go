package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func downgradeWorkboardEventsTo35(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`ALTER TABLE workboard_events RENAME TO workboard_events_v36;
		DROP INDEX workboard_events_operation;
		CREATE TABLE workboard_events(
		 id TEXT NOT NULL UNIQUE CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
		 board_id TEXT NOT NULL REFERENCES workboard_boards(id) ON DELETE CASCADE,
		 sequence INTEGER NOT NULL CHECK(sequence>0),
		 operation_id TEXT NOT NULL,
		 kind TEXT NOT NULL CHECK(length(CAST(kind AS BLOB)) BETWEEN 1 AND 128),
		 actor_id TEXT NOT NULL CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 128),
		 actor_type TEXT NOT NULL CHECK(actor_type IN('operator','worker','validator','model','system')),
		 created_at INTEGER NOT NULL CHECK(created_at>=0),
		 body BLOB NOT NULL CHECK(length(body) BETWEEN 1 AND 1048576),
		 PRIMARY KEY(board_id,sequence),
		 FOREIGN KEY(board_id,operation_id) REFERENCES workboard_operations(board_id,operation_id) DEFERRABLE INITIALLY DEFERRED);
		INSERT INTO workboard_events(id,board_id,sequence,operation_id,kind,actor_id,actor_type,created_at,body)
		 SELECT id,board_id,sequence,operation_id,kind,actor_id,actor_type,created_at,body FROM workboard_events_v36;
		DROP TABLE workboard_events_v36;
		CREATE INDEX workboard_events_operation ON workboard_events(operation_id,sequence);
		DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=35;`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkboardEventCardMigrationUpgradesExisting35(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 0, event("workboard-task", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	downgradeWorkboardEventsTo35(t, store.db)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, eventCount int
	var cardID sql.NullString
	var id, boardID, operationID, kind, actorID, actorType string
	var sequence, createdAt int64
	var body []byte
	if err = store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentStorageSchema {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	if err = store.db.QueryRow(`SELECT id,board_id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
		FROM workboard_events WHERE board_id='board' AND sequence=1`).Scan(&id, &boardID, &sequence, &operationID, &kind, &actorID, &actorType, &cardID, &createdAt, &body); err != nil {
		t.Fatal(err)
	}
	if id != "event" || boardID != "board" || sequence != 1 || operationID != "operation" || kind != "board.created" || actorID != "operator" ||
		actorType != "operator" || cardID.Valid || createdAt != 1 || string(body) != "{}" {
		t.Fatalf("v35 event changed: %q %q %d %q %q %q/%q card=%+v at=%d body=%q", id, boardID, sequence, operationID, kind, actorID, actorType, cardID, createdAt, body)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_events`).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("event count=%d err=%v", eventCount, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatal("restart after v36 migration", err)
	}
	if err = restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkboardEventCardMigrationRejectsForgedPartial36Atomically(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 0, event("workboard-task", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	insertWorkboardFixture(t, store.db)
	downgradeWorkboardEventsTo35(t, store.db)
	if _, err = store.db.Exec(`ALTER TABLE workboard_events ADD COLUMN card_id TEXT`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("forged partial schema 36 accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, rows int
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 35 {
		t.Fatalf("failed migration advanced version=%d err=%v", version, err)
	}
	if err = raw.QueryRow(`SELECT count(*) FROM workboard_events`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("failed migration changed rows=%d err=%v", rows, err)
	}
	var foreignKeys int
	if err = raw.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list('workboard_events') WHERE "from"='card_id'`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("failed migration repaired forged schema foreign_keys=%d err=%v", foreignKeys, err)
	}
}
