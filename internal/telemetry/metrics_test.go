package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestMetricsAccountingReconcilesRoutedAndAuxiliaryUsage(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	_ = usageTask(t, db, "accounted-task", false, &providers.Usage{InputTokens: 7, OutputTokens: 3}, runtime.TaskCompleted)
	review := evaluation.ReviewAttempt{Version: 1, ID: "metrics-review", TaskID: "accounted-task", AttemptID: "candidate", EvaluatorModel: "judge-model", EvaluatorProvider: "judge-provider", EstimatedCost: .5, Status: "started", StartedAt: time.Date(2026, 9, 8, 12, 1, 0, 0, time.UTC)}
	if err := db.BeginReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	review.Status, review.Code, review.FinishedAt = "failed", "review_failed", review.StartedAt.Add(time.Second)
	if err := db.FinishReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil || snapshot.Accounting == nil {
		t.Fatal(snapshot, err)
	}
	totals := snapshot.Accounting
	if totals.Primary.Records != 1 || totals.Judge.Records != 1 || totals.Routed.Records != 1 || totals.Auxiliary.Records != 1 || totals.Overall.Records != 2 || totals.Overall.KnownInputTokens != 7 || totals.Overall.KnownOutputTokens != 3 || totals.Overall.UnknownUsageRecords != 1 {
		t.Fatal("accounting did not reconcile", totals)
	}
}

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
	if _, err := db.db.Exec(`INSERT INTO evaluations VALUES('evaluation','running','attempt','model','provider','domain','profile',zeroblob(9000000)); INSERT INTO audit_records VALUES('audit','running',json_set(json_object('Audit',json_object('verdict','accept')),'$.private',hex(zeroblob(4500000))))`); err != nil {
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
	if snapshot.StorageSchema != 33 || snapshot.Validate() != nil {
		t.Fatal(snapshot)
	}
	for _, group := range snapshot.Groups {
		if group.Name == "queue_age" {
			var total int64
			for _, count := range group.Counts {
				total += count.Value
			}
			if total != 1 {
				t.Fatal(group)
			}
			continue
		}
		for _, count := range group.Counts {
			expected := int64(1)
			if group.Name == "runtime_events" || group.Name == "runtime_operations" {
				expected = 0
			}
			if group.Name == "audit_outcomes" && count.State != "accept" {
				expected = 0
			}
			if count.Value != expected {
				t.Fatalf("%s %s %d", group.Name, count.State, count.Value)
			}
		}
	}
	body, _ := json.Marshal(snapshot)
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "private-token") {
		t.Fatal("payload leaked")
	}
}

func TestMetricsQueueAgeDistributionAndInvalidTime(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	created := []time.Time{
		now.Add(-2 * time.Hour), now.Add(-45 * time.Minute), now.Add(-10 * time.Minute),
		now.Add(-2 * time.Minute), now.Add(-30 * time.Second), now.Add(-5 * time.Second),
		now.Add(time.Hour), now,
	}
	for i, at := range created {
		job := queuedSubmission(t, db, fmt.Sprintf("queue-age-%d", i))
		if i == len(created)-1 {
			at = time.Now().UTC()
		}
		value := at.Format(time.RFC3339Nano)
		if _, err := db.db.Exec(`UPDATE submissions SET created_at=? WHERE id=?`, value, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	corrupt := queuedSubmission(t, db, "queue-age-corrupt")
	if _, err := db.db.Exec(`UPDATE submissions SET created_at='not-a-time' WHERE id=?`, corrupt.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil {
		t.Fatal(snapshot, err)
	}
	for _, group := range snapshot.Groups {
		if group.Name != "queue_age" {
			continue
		}
		for _, count := range group.Counts {
			want := int64(1)
			if count.State == "invalid_time" {
				want = 2
			}
			if count.Value != want {
				t.Fatal(group)
			}
		}
		body, _ := json.Marshal(group)
		if strings.Contains(string(body), "queue-age") {
			t.Fatal("queue identity leaked", string(body))
		}
		return
	}
	t.Fatal("queue age group missing")
}

func TestMetricsQueueAgeRejectsPopulationBeyondAdmissionBound(t *testing.T) {
	db, _ := submissionStore(t)
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.Prepare(`INSERT INTO submissions(id,key_digest,request_digest,config_digest,request,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i <= submissions.MaxQueued; i++ {
		id := fmt.Sprintf("overbound-%03d", i)
		if _, err := statement.Exec(id, submitDigest("key-"+id), submitDigest("{}"), submitDigest("config"), []byte("{}"), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Metrics(context.Background()); err == nil {
		t.Fatal("overbound queue accepted")
	}
}

func TestMetricsCountsAdvisoryAuditOutcomesWithoutIdentity(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if _, err := db.db.Exec(`INSERT INTO task_heads VALUES('audit-task','private-session',1,'completed')`); err != nil {
		t.Fatal(err)
	}
	for _, verdict := range []string{"accept", "reject", "abstain"} {
		body, err := json.Marshal(map[string]any{"Audit": map[string]any{"verdict": verdict}, "private": "private-finding-" + verdict})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO audit_records VALUES(?,?,?)`, "private-audit-"+verdict, "audit-task", body); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := db.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil {
		t.Fatal(snapshot, err)
	}
	for _, group := range snapshot.Groups {
		if group.Name != "audit_outcomes" {
			continue
		}
		for _, count := range group.Counts {
			if count.Value != 1 {
				t.Fatal(group)
			}
		}
		body, _ := json.Marshal(group)
		if strings.Contains(string(body), "private") || strings.Contains(string(body), "finding") {
			t.Fatal("audit content escaped", string(body))
		}
		return
	}
	t.Fatal("audit outcomes group missing")
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
		data := map[string]any{"text": "private-payload"}
		if kind == "task.started" {
			data["retry_of_task_id"] = "private-prior-task"
			data["compaction"] = map[string]any{"summary": "private-summary"}
			data["skill_context"] = map[string]any{"references": []string{"private-skill"}}
		}
		if kind == "route.selected" {
			data["route"] = map[string]any{"Explored": true, "Excluded": []map[string]any{
				{"Model": "private-capacity", "Reasons": []string{"capacity"}},
				{"Model": "private-budget", "Reasons": []string{"budget"}},
				{"Model": "private-privacy", "Reasons": []string{"privacy"}},
				{"Model": "private-health", "Reasons": []string{"health"}},
			}}
		}
		body, err := json.Marshal(map[string]any{"kind": kind, "data": data})
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
	for _, group := range snapshot.Groups {
		if group.Name != "runtime_operations" {
			continue
		}
		if len(group.Counts) != 8 {
			t.Fatal(group)
		}
		for _, count := range group.Counts {
			if count.Value != 1 {
				t.Fatal("derived operation missing", count)
			}
		}
	}
	body, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(body), "private") {
		t.Fatal("runtime event metric leaked payload", err)
	}
}

func TestMetricsLegacyAbsentTables(t *testing.T) {
	db, path := submissionStore(t)
	if _, err := db.db.Exec(`DROP TABLE task_timings; DROP TABLE task_timing_metadata; DROP INDEX events_task_kind; DROP TABLE skill_exposures; DROP INDEX task_heads_session; DROP TABLE learning_activation_intents; DROP TABLE lease_attention_history; DROP TABLE lease_attention; DROP TABLE lease_recoveries; ALTER TABLE resource_leases DROP COLUMN process_id; DROP TABLE lease_processes; DROP TABLE learning_states; DROP TABLE memory_retired_ids; DROP TABLE workflow_scan_buckets; DROP TABLE workflow_scan_consumptions; DROP TABLE workflow_scan_consumers; DROP TRIGGER workflow_scan_task_insert; DROP TABLE workflow_scan_tasks; DROP TABLE workflow_scan_pages; DROP TABLE workflow_scans; DROP TABLE workflow_selections; DROP TABLE skill_generation_attempts; DROP TABLE tool_approvals; DROP TABLE task_steering; DROP TABLE submission_recoveries; DROP TABLE submissions; DROP TABLE review_attempts; DROP TABLE audit_records; DROP TABLE evaluations; DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; DROP INDEX IF EXISTS evaluations_routing_key; PRAGMA user_version=1`); err != nil {
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
		if group.Name != "tasks" && group.Name != "runtime_events" && group.Name != "runtime_operations" && (group.Available || len(group.Counts) != 0) {
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
		since := map[string]int{"tasks": 1, "runtime_events": 1, "runtime_operations": 1, "submissions": 12, "queue_age": 12, "reviews": 7, "evaluations": 2, "audits": 5, "audit_outcomes": 5, "recoveries": 13}
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
	for _, table := range []string{"task_heads", "events", "submissions", "review_attempts", "audit_records"} {
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
			case "audit_records":
				_, err = db.db.Exec(`INSERT INTO audit_records VALUES('audit','task',json_object('Audit',json_object('verdict',?)))`, secret)
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
