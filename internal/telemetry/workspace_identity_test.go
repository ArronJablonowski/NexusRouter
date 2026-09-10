package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkspaceIdentityPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.WorkspaceIdentity(ctx)
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
	second, err := store.WorkspaceIdentity(ctx)
	if err != nil || second != first {
		t.Fatalf("identity changed across restart: first=%q second=%q err=%v", first, second, err)
	}
}

func TestWorkspaceIdentityMigrationRejectsForgedTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "forged.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TABLE workspace_identity; CREATE TABLE workspace_identity(singleton INTEGER PRIMARY KEY,id TEXT); DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=36`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("forged workspace identity schema accepted")
	}
}
