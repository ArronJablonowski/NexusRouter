package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMetricsCountsAndPayloadIsolation(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		if _, err := db.db.Exec(`INSERT INTO task_heads VALUES(?,?,1,?)`, state, "private-session", state); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"queued", "running", "succeeded", "failed", "canceled"} {
		job := queuedSubmission(t, db, state)
		if _, err := db.db.Exec(`UPDATE submissions SET state=?,request=zeroblob(9000000),result=zeroblob(9000000),token='private-token' WHERE id=?`, state, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"started", "completed", "failed"} {
		if _, err := db.db.Exec(`INSERT INTO review_attempts VALUES(?,'running',?,zeroblob(9000000))`, state, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`INSERT INTO evaluations VALUES('evaluation','running','attempt','model','provider','domain','profile',zeroblob(9000000)); INSERT INTO audit_records VALUES('audit','running',zeroblob(9000000))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO submission_recoveries SELECT 'recovery',id,'private-digest',zeroblob(9000000) FROM submissions LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	snapshot, err := ro.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StorageSchema != 29 || snapshot.Validate() != nil {
		t.Fatal(snapshot)
	}
	for _, group := range snapshot.Groups {
		for _, count := range group.Counts {
			expected := int64(1)
			if group.Name == "runtime_events" {
				expected = 0
			}
			if count.Value != expected {
				t.Fatalf("%s %s %d", group.Name, count.State, count.Value)
			}
		}
	}
	body, _ := json.Marshal(snapshot)
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "token") {
		t.Fatal("payload leaked")
	}
}

func TestMetricsCountsCanonicalRuntimeEvents(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if _, err := db.db.Exec(`INSERT INTO task_heads VALUES('event-task','private-session',16,'running')`); err != nil {
		t.Fatal(err)
	}
	kinds := []string{
		"task.started", "task.completed", "task.failed", "task.canceled",
		"turn.started", "turn.completed", "model.delta", "tool.started", "tool.completed",
		"worker.started", "worker.heartbeat", "worker.completed", "route.selected",
		"evaluation.recorded", "error.recorded", "steering.applied",
	}
	for i, kind := range kinds {
		body, err := json.Marshal(map[string]any{"kind": kind, "data": map[string]string{"text": "private-payload"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.db.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,?,?)`, fmt.Sprintf("event-%d", i), "event-task", i+1, body); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := db.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil {
		t.Fatal(snapshot, err)
	}
	var found bool
	for _, group := range snapshot.Groups {
		if group.Name != "runtime_events" {
			continue
		}
		found = true
		if len(group.Counts) != len(kinds) {
			t.Fatal(group)
		}
		for i, count := range group.Counts {
			if count.State != kinds[i] || count.Value != 1 {
				t.Fatal(count)
			}
		}
	}
	if !found {
		t.Fatal("runtime event group missing")
	}
	body, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(body), "private") {
		t.Fatal("runtime event metric leaked payload", err)
	}
}

func TestMetricsLegacyAbsentTables(t *testing.T) {
	db, path := submissionStore(t)
	if _, err := db.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE review_attempts; DROP TABLE audit_records; DROP TABLE evaluations; PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	snapshot, err := ro.Metrics(context.Background())
	if err != nil || snapshot.StorageSchema != 1 {
		t.Fatal(snapshot, err)
	}
	for _, group := range snapshot.Groups {
		if group.Name != "tasks" && group.Name != "runtime_events" && (group.Available || len(group.Counts) != 0) {
			t.Fatal(group)
		}
	}
	var version int
	if err = ro.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("migrated %d %v", version, err)
	}
}

func TestMetricsLegacyAvailabilityAndCancellation(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	for _, schema := range []int{1, 2, 5, 7, 12, 13} {
		if _, err := db.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schema)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := db.Metrics(ctx)
		if err != nil || snapshot.StorageSchema != schema {
			t.Fatal(snapshot, err)
		}
		since := map[string]int{"tasks": 1, "runtime_events": 1, "submissions": 12, "reviews": 7, "evaluations": 2, "audits": 5, "recoveries": 13}
		for _, group := range snapshot.Groups {
			if group.Available != (schema >= since[group.Name]) {
				t.Fatal(group)
			}
			for _, count := range group.Counts {
				if count.Value != 0 {
					t.Fatal(count)
				}
			}
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.Metrics(canceled); err == nil {
		t.Fatal("canceled read accepted")
	}
}

func TestMetricsRejectUnknownLifecycleState(t *testing.T) {
	for _, table := range []string{"task_heads", "events", "submissions", "review_attempts"} {
		t.Run(table, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			if _, err := db.db.Exec(`INSERT INTO task_heads VALUES('task','session',1,'running')`); err != nil {
				t.Fatal(err)
			}
			secret := strings.Repeat("private", 100000)
			var err error
			switch table {
			case "task_heads":
				_, err = db.db.Exec(`UPDATE task_heads SET state=?`, secret)
			case "events":
				_, err = db.db.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES('event','task',1,json_object('kind',?))`, secret)
			case "submissions":
				queuedSubmission(t, db, "job")
				_, err = db.db.Exec(`UPDATE submissions SET state=?`, secret)
			case "review_attempts":
				_, err = db.db.Exec(`INSERT INTO review_attempts VALUES('review','task',?,'{}')`, secret)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Metrics(ctx); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("corrupt state error %v", err)
			}
		})
	}
}
