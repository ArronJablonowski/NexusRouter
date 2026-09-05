package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darwinrouter/internal/telemetry"
	"darwinrouter/providers"
	"darwinrouter/routing"
	"darwinrouter/runtime"
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

func TestFeedbackRejectsNonCompletedHistory(t *testing.T) {
	for _, terminal := range []runtime.Kind{"", runtime.TaskFailed, runtime.TaskCanceled} {
		path := filepath.Join(t.TempDir(), "tasks.db")
		db, err := telemetry.Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted}
		if terminal != "" {
			kinds = append(kinds, terminal)
		}
		for i, kind := range kinds {
			e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind, Data: runtime.Data{ModelID: "m", ProviderID: "p"}}
			if i == 0 {
				e.Data.Messages = []providers.Message{{Role: "user", Content: "hello"}}
			} else {
				e.TurnID = "turn"
				e.AttemptID = "attempt"
			}
			if err := db.Append(context.Background(), int64(i), e); err != nil {
				t.Fatal(err)
			}
		}
		if err := RecordFeedback(context.Background(), path, "task", true, 0); !errors.Is(err, ErrAdmission) {
			t.Fatalf("%s admitted: %v", terminal, err)
		}
		if _, err := db.Fitness(context.Background(), routing.Key{Model: "m", Provider: "p", Domain: "general", Profile: "default"}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("invalid history affected fitness", err)
		}
		db.Close()
	}
}

func TestExplicitFeedbackKeepsDomainAndProfile(t *testing.T) {
	svc, cfg := autoFixture(t)
	out, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello", Domain: "review", Profile: "careful"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordFeedback(context.Background(), cfg.Telemetry.Database, out.TaskID, true, .25); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e, err := db.Fitness(context.Background(), routing.Key{Model: "a", Provider: "local", Domain: "review", Profile: "careful"})
	if err != nil || e.Samples != 1 || e.Quality != 1 || e.Cost != .25 {
		t.Fatalf("%+v %v", e, err)
	}
}
