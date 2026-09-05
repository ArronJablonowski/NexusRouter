package telemetry

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const modelStartLookup = `SELECT task_id,sequence FROM events WHERE json_extract(body,'$.kind')='turn.started' AND json_extract(body,'$.data.model_id')=? AND json_extract(body,'$.data.provider_id')=?`

func TestEventModelIndexMigrationAndAppend(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	start := event("task-start", 1, runtime.TaskStarted)
	if err := s.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	turn := event("turn-one", 2, runtime.TurnStarted)
	turn.TurnID, turn.AttemptID = "turn-one", "attempt-one"
	turn.Data = runtime.Data{ModelID: "model", ProviderID: "provider"}
	if err := s.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	before, err := s.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP INDEX events_submission_start; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE task_cancellations; DROP TABLE summary_review_heads; DROP TABLE summary_reviews; DROP TABLE summary_attempts; DROP INDEX events_model_start; PRAGMA user_version=7;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 18 {
		t.Fatal(version, err)
	}
	after, err := s.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("migration changed events", after, err)
	}
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+modelStartLookup, "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	usesIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		usesIndex = usesIndex || strings.Contains(detail, "USING INDEX events_model_start")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !usesIndex {
		t.Fatal("model lookup did not use expression index")
	}
	turn.ID, turn.TurnID, turn.AttemptID, turn.Sequence = "turn-two", "turn-two", "attempt-two", 3
	if err := s.Append(ctx, 2, turn); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+modelStartLookup+")", "model", "provider").Scan(&count); err != nil || count != 2 {
		t.Fatal("append not indexed", count, err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+modelStartLookup+")", "missing", "provider").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+modelStartLookup+")", "model", "provider").Scan(&count); err != nil || count != 2 {
		t.Fatal("read-only index", count, err)
	}
}
