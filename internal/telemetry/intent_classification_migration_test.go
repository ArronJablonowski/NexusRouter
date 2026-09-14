package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestIntentClassificationAttemptMigrationFrom45IsEmptyAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE intent_classification_attempts; PRAGMA user_version=45`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, attempts int
	if err = store.db.QueryRow(`SELECT
		(SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM intent_classification_attempts)`).Scan(&version, &attempts); err != nil ||
		version != currentStorageSchema || attempts != 0 {
		t.Fatalf("schema=%d attempts=%d err=%v", version, attempts, err)
	}
}

func TestIntentClassificationAttemptMigrationRejectsRetainedFutureTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO intent_classification_attempts
		(id,task_id,session_id,submission_id,status,body)
		VALUES('attempt','task','session','','started','{}'); PRAGMA user_version=45`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("schema-45 database retained schema-46 classifier attempt table")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, tables int
	if err = raw.QueryRow(`SELECT
		(SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='intent_classification_attempts')`).
		Scan(&version, &tables); err != nil || version != 45 || tables != 1 {
		t.Fatalf("schema=%d tables=%d err=%v", version, tables, err)
	}
}

func TestIntentClassificationAttemptSchema46RejectsCorruptShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE intent_classification_attempts;
		CREATE TABLE intent_classification_attempts(sentinel TEXT);
		PRAGMA user_version=46`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("corrupt schema-46 classifier attempt table accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 46 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
}
