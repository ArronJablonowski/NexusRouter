package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"darwinrouter/internal/telemetry"
)

func TestAuditInspectionDoesNotCreateStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if runAudits([]string{"list", "--db", path, "--task", "task"}, io.Discard, io.Discard) != 1 {
		t.Fatal("missing store admitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created storage")
	}
}

func TestAuditInspectionListAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if runAudits([]string{"list", "--db", path, "--task", "task"}, io.Discard, io.Discard) != 0 {
		t.Fatal("empty listing failed")
	}
	if runAudits([]string{"attempts", "--db", path, "--task", "task"}, io.Discard, io.Discard) != 0 {
		t.Fatal("empty attempt listing failed")
	}
	for _, args := range [][]string{{"delete"}, {"show", "--db", path}, {"list", "--db", path, "--task", "task", "--limit", "101"}} {
		if runAudits(args, io.Discard, io.Discard) != 2 {
			t.Fatal(args)
		}
	}
}
