package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestLearningMigrationAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "learning.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=20`); err != nil {
		t.Fatal(err)
	}
	fact := testFact()
	if err = s.PutMemory(ctx, fact, 0); err != nil {
		t.Fatal(err)
	}
	retired := fact
	retired.ID = "retired-fact"
	retired.Content = "Deleted before learning migration"
	if err = s.PutMemory(ctx, retired, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteMemory(ctx, retired.Scope, retired.ID, retired.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 33 {
		t.Fatal(version, err)
	}
	if got, err := s.GetMemory(ctx, fact.Scope, fact.ID); err != nil || got != fact {
		t.Fatal("migration changed memory fact", got, err)
	}
	if got, err := s.GetMemory(ctx, retired.Scope, retired.ID); !errors.Is(err, memory.ErrConflict) || got != (memory.Fact{}) {
		t.Fatal("migration restored deleted content", got, err)
	}
	var retiredScope, retiredID string
	if err = s.db.QueryRow(`SELECT scope,id FROM memory_retired_ids`).Scan(&retiredScope, &retiredID); err != nil || retiredScope != retired.Scope || retiredID != retired.ID {
		t.Fatal("migration changed retired identity", retiredScope, retiredID, err)
	}
	if err = s.PutMemory(ctx, retired, 0); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("migration permitted retired identity reuse", err)
	}
	state := learningFixture()
	if err = s.PutLearningState(ctx, state, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_learning BEFORE UPDATE OF body ON learning_states BEGIN SELECT RAISE(ABORT,'private'); END`); err != nil {
		t.Fatal(err)
	}
	next := state
	next.Revision++
	next.Phase = "consume"
	next.ScanRevision = 1
	next.Epoch = 1
	if err = s.PutLearningState(ctx, next, 1); err != ErrLearningUnavailable {
		t.Fatal(err)
	}
	if got, err := s.LearningState(ctx, state.Scope, state.Name); err != nil || got != state {
		t.Fatal(got, err)
	}
}
