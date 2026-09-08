package telemetry

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestEventKindIndexMigration27PreservesEvidenceAndReadonly(t *testing.T) {
	ctx := context.Background()
	s, path := generationStore(t)
	appendValidity(t, s, validityEvents("first", true))
	before := workflowSourceRawBodies(t, s)
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=27`); err != nil {
		t.Fatal(err)
	}
	old, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := old.OutputValidity(ctx, validityKey())
	if err != nil || got.Samples != 1 || got.Failures != 0 {
		t.Fatal(got, err)
	}
	old.Close()
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='events_task_kind'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("readonly migrated", count, err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var schema int
	var definition string
	if err = migrated.db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil || schema != 31 {
		t.Fatal(schema, err)
	}
	if err = migrated.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='events_task_kind' AND type='index'`).Scan(&definition); err != nil || !strings.Contains(definition, "json_extract(body,'$.kind')") {
		t.Fatal(definition, err)
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, migrated)) {
		t.Fatal("migration changed source bodies")
	}
	if got, err = migrated.OutputValidity(ctx, validityKey()); err != nil || got.Samples != 1 || got.Failures != 0 {
		t.Fatal(got, err)
	}
	appendValidity(t, migrated, validityEvents("second", false))
	var indexed int
	if err = migrated.db.QueryRow(`SELECT count(*) FROM events INDEXED BY events_task_kind WHERE task_id=? AND json_extract(body,'$.kind')=?`, "second", runtime.EvaluationRecorded).Scan(&indexed); err != nil || indexed != 1 {
		t.Fatal("append omitted index", indexed, err)
	}
	if got, err = migrated.OutputValidity(ctx, validityKey()); err != nil || got.Samples != 2 || got.Failures != 1 {
		t.Fatal(got, err)
	}
	stable := workflowSourceRawBodies(t, migrated)
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(stable, workflowSourceRawBodies(t, reopened)) {
		t.Fatal("repeat migration changed history")
	}
	// Confirm the production expression is a usable exact-kind search key.
	rows, err := reopened.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT max(sequence) FROM events WHERE task_id=? AND json_extract(body,'$.kind')='turn.started'`, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	used := false
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		used = used || strings.Contains(detail, "events_task_kind")
	}
	if err = rows.Err(); err != nil || !used {
		t.Fatal("index not used", err)
	}
}

func TestEventKindIndexFailedMigrationRemains27AndCanReopen(t *testing.T) {
	ctx := context.Background()
	s, path := generationStore(t)
	appendValidity(t, s, validityEvents("first", true))
	before := workflowSourceRawBodies(t, s)
	// A conflicting schema object fails CREATE INDEX inside the migration, after
	// discovery under BEGIN IMMEDIATE, without modifying the legacy journal.
	if _, err := s.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; CREATE TABLE events_task_kind(sentinel TEXT); INSERT INTO events_task_kind VALUES('retained'); DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=27`); err != nil {
		t.Fatal(err)
	}
	if bad, err := Open(ctx, path); err == nil {
		bad.Close()
		t.Fatal("failed migration accepted")
	}
	var schema int
	var sentinel string
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil || schema != 27 {
		t.Fatal(schema, err)
	}
	if err := s.db.QueryRow(`SELECT sentinel FROM events_task_kind`).Scan(&sentinel); err != nil || sentinel != "retained" {
		t.Fatal(sentinel, err)
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, s)) {
		t.Fatal("failed migration changed evidence")
	}
	if _, err := s.db.Exec(`DROP TABLE events_task_kind`); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil || schema != 31 {
		t.Fatal(schema, err)
	}
	if !reflect.DeepEqual(before, workflowSourceRawBodies(t, reopened)) {
		t.Fatal("recovery changed evidence")
	}
	// Repeated opens must not recreate or duplicate the index.
	var n sql.NullInt64
	if err = reopened.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='events_task_kind' AND type='index'`).Scan(&n); err != nil || !n.Valid || n.Int64 != 1 {
		t.Fatal(n, err)
	}
}
