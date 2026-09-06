package telemetry

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestMemoryRetirementSurvivesReopenAndScopes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f := testFact()
	if err = s.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteMemory(ctx, f.Scope, f.ID, 1); err != nil {
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
	if err = s.PutMemory(ctx, f, 0); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("retired ID recreated", err)
	}
	if err = s.TouchMemoryFact(ctx, f, f.Created.Add(time.Second)); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("retired snapshot touched", err)
	}
	var scope, id string
	if err = s.db.QueryRow("SELECT scope,id FROM memory_retired_ids").Scan(&scope, &id); err != nil || scope != f.Scope || id != f.ID {
		t.Fatal(scope, id, err)
	}
	f.Scope = "different-scope"
	if err = s.PutMemory(ctx, f, 0); err != nil {
		t.Fatal("retirement crossed scope", err)
	}
}

func TestMemoryRetirementStaleDeleteDoesNotRetire(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f := testFact()
	if err = s.DeleteMemory(ctx, f.Scope, f.ID, 1); !errors.Is(err, memory.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.PutMemory(ctx, f, 0); err != nil {
		t.Fatal("failed absent delete retired ID", err)
	}
	if err = s.DeleteMemory(ctx, f.Scope, f.ID, 2); !errors.Is(err, memory.ErrConflict) {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM memory_retired_ids").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	f.Revision = 2
	f.Updated = f.Updated.Add(time.Second)
	if err = s.PutMemory(ctx, f, 1); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryRetirementExpirationAndAtomicRollback(t *testing.T) {
	for _, expiration := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "expire"}[expiration], func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "memory.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			f := testFact()
			f.Expires = f.Created.Add(time.Second)
			if err = s.PutMemory(ctx, f, 0); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("CREATE TRIGGER fail_memory_delete BEFORE DELETE ON memory_facts BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
				t.Fatal(err)
			}
			remove := func() error {
				if expiration {
					_, err := s.ExpireMemory(ctx, f.Scope, f.Expires)
					return err
				}
				return s.DeleteMemory(ctx, f.Scope, f.ID, 1)
			}
			if remove() == nil {
				t.Fatal("trigger ignored")
			}
			var count int
			if err = s.db.QueryRow("SELECT count(*) FROM memory_retired_ids").Scan(&count); err != nil || count != 0 {
				t.Fatal("partial retirement committed", count, err)
			}
			if _, err = s.GetMemory(ctx, f.Scope, f.ID); err != nil {
				t.Fatal("rollback lost fact", err)
			}
			if _, err = s.db.Exec("DROP TRIGGER fail_memory_delete"); err != nil {
				t.Fatal(err)
			}
			if err = remove(); err != nil {
				t.Fatal(err)
			}
			if err = s.PutMemory(ctx, f, 0); !errors.Is(err, memory.ErrConflict) {
				t.Fatal("retired expired ID reused", err)
			}
		})
	}
}

func TestMemoryRetirementConcurrentDeleteRecreate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	f := testFact()
	if err = s.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 10 {
				if err := other.PutMemory(ctx, f, 0); !errors.Is(err, memory.ErrConflict) {
					t.Errorf("concurrent recreate accepted: %v", err)
				}
			}
		}()
	}
	close(start)
	if err = s.DeleteMemory(ctx, f.Scope, f.ID, 1); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err = s.GetMemory(ctx, f.Scope, f.ID); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("fact resurrected", err)
	}
}

func TestMemoryRetirementMigrationPreservesPayload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f := testFact()
	if err = s.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err = s.db.QueryRow("SELECT body FROM memory_facts").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; PRAGMA user_version=19"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if control, err := OpenMemoryControl(ctx, path); err == nil {
		control.Close()
		t.Fatal("control silently migrated")
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var after []byte
	var version, count int
	if err = s.db.QueryRow("SELECT body FROM memory_facts").Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("migration changed payload", err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 29 {
		t.Fatal(version, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM memory_retired_ids").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented retired IDs", count, err)
	}
}
