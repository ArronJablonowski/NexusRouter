package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"darwinrouter/internal/telemetry"
	"darwinrouter/routing"
)

func TestFeedbackUpdatesRoutingExactlyOnce(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{Prompt: "hello", Domain: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e, err := db.Fitness(ctx, routing.Key{Model: out.Text, Provider: "local", Domain: "coding", Profile: "default"})
	if err != nil || e.Samples != 1 || e.Quality != 0 || e.Reliability != 1 {
		t.Fatalf("%+v %v", e, err)
	}
	if err := RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, true, 0); err == nil {
		t.Fatal("conflicting feedback accepted")
	}
	if err := RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, false, 1); err == nil {
		t.Fatal("conflicting cost accepted")
	}
}

func TestFeedbackMissingDatabaseNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if err := RecordFeedback(context.Background(), path, "task", true, 0); err == nil {
		t.Fatal("missing task accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created missing database")
	}
}
