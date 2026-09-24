package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCurrentSchemaDoesNotWaitForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A heartbeat or event append may own the writer reservation. Opening a
	// second handle for current-schema validation must remain independent.
	if _, err = tx.Exec("UPDATE submissions SET id=id WHERE id=''"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("current-schema read validation blocked behind writer: %v", err)
	}
	other.Close()
}
