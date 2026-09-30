package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestFeedbackUsesWholeTaskLatencyAndPreservesLegacyRetries(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		ctx := context.Background()
		db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "latency.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		base := time.Unix(100, 0).UTC()
		kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted}
		seconds := []int{0, 1, 50, 51, 60, 60}
		for i, kind := range kinds {
			e := runtime.Event{Version: 1, ID: string(rune('a' + i)), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: base.Add(time.Duration(seconds[i]) * time.Second), Kind: kind}
			if i == 0 {
				e.Data.Messages = []providers.Message{{Role: "user", Content: "work"}}
				e.Data.ContextTokens = 32768
			}
			if i == 1 || i == 2 {
				e.TurnID = "one"
				e.AttemptID = "first"
			}
			if i == 3 || i == 4 {
				e.TurnID = "two"
				e.AttemptID = "last"
			}
			if kind == runtime.TurnStarted {
				e.Data.ModelID = "m"
				e.Data.ProviderID = "p"
			}
			if err = db.Append(ctx, int64(i), e); err != nil {
				t.Fatal(err)
			}
		}
		want := time.Minute
		if legacy {
			hash := sha256.Sum256([]byte("user-feedback:task:last"))
			id := hex.EncodeToString(hash[:])
			want = 9 * time.Second
			r := evaluation.Record{Version: 1, ID: id, TaskID: "task", AttemptID: "last", Key: routing.Key{Model: "m", Provider: "p", Domain: "general", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: id, Passed: false}}, ExecutionSucceeded: true, Latency: want, ContextTokens: 32768, Time: base.Add(time.Minute)}
			if err = db.RecordEvaluation(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			if err = RecordFeedbackStore(ctx, db, "task", false, 0); err != nil {
				t.Fatal(err)
			}
		}
		got, err := db.EvaluationHistory(ctx, "task", "last")
		if err != nil || len(got) != 1 || got[0].Latency != want {
			t.Fatalf("legacy=%v: %+v %v", legacy, got, err)
		}
	}
}

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
