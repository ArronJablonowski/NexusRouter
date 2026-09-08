package telemetry

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func downgradeObservationIndex(t *testing.T, db *Store) {
	t.Helper()
	if _, err := db.db.Exec(`DROP TABLE submission_stream_events; DROP INDEX evaluations_routing_key; PRAGMA user_version=30`); err != nil {
		t.Fatal(err)
	}
}

func TestObservationIndexMigrationPreservesSchema30DataAndPlan(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema30.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := revisionBase(t, db)
	downgradeObservationIndex(t, db)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var schema, indexes int
	if err = db.db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err = db.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='evaluations_routing_key' AND sql LIKE '%model,provider,domain,profile,id%'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	set, err := db.ObservationSet(ctx, base.Key)
	if err != nil || schema != 32 || indexes != 1 || len(set.Fitness) != 1 || set.Fitness[0].ID != base.ID {
		t.Fatal("schema30 observation migration changed evidence", schema, indexes, set, err)
	}

	rows, err := db.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+fitnessObservationQuery, base.Key.Model, base.Key.Provider, base.Key.Domain, base.Key.Profile, base.Key.Model, base.Key.Provider, base.Key.Domain, base.Key.Profile, maxRoutingObservations+1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan, "SCAN e") || strings.Contains(plan, "SCAN r") || !strings.Contains(plan, "evaluations_routing_key") || !strings.Contains(plan, "evaluation_revisions_base") {
		t.Fatalf("routing observation plan is not key-first:\n%s", plan)
	}
}

func TestObservationIndexMigrationConflictIsAtomic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conflict.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeObservationIndex(t, db)
	if _, err = db.db.Exec(`CREATE INDEX evaluations_routing_key ON task_heads(task_id); CREATE TABLE observation_sentinel(value TEXT); INSERT INTO observation_sentinel VALUES('preserved')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("conflicting preexisting index accepted")
	}
	read, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	var schema int
	var sentinel string
	if err = read.db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil || schema != 30 {
		t.Fatal("failed migration changed schema", schema, err)
	}
	if err = read.db.QueryRow(`SELECT value FROM observation_sentinel`).Scan(&sentinel); err != nil || sentinel != "preserved" {
		t.Fatal("failed migration changed prior data", sentinel, err)
	}
}

func TestObservationIndexMigrationConcurrent30To31(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeObservationIndex(t, db)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	stores := make(chan *Store, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opened, openErr := Open(ctx, path)
			if openErr == nil {
				stores <- opened
			}
			errs <- openErr
		}()
	}
	wg.Wait()
	close(errs)
	close(stores)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for opened := range stores {
		defer opened.Close()
		var schema, indexes int
		if opened.db.QueryRow(`PRAGMA user_version`).Scan(&schema) != nil || opened.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='evaluations_routing_key'`).Scan(&indexes) != nil || schema != 32 || indexes != 1 {
			t.Fatal("concurrent migration did not converge", schema, indexes)
		}
	}
}
