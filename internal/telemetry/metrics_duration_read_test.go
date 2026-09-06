package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func metricsDurationTask(t *testing.T, db *Store, id string, duration time.Duration, terminal runtime.Kind) {
	t.Helper()
	start := event(id+"-start", 1, runtime.TaskStarted)
	start.TaskID, start.SessionID = id, "private-session-"+id
	start.Data.Text = "private-prompt"
	if err := db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	end := start
	end.ID, end.Sequence, end.Kind, end.Time = id+"-end", 2, terminal, start.Time.Add(duration)
	end.Data.Text = "private-output"
	if err := db.Append(context.Background(), 1, end); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsDurationExactBucketsAndUnavailable(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	metricsDurationTask(t, db, "zero", 0, runtime.TaskCompleted)
	var expectedSum float64
	for i, bound := range metrics.TaskDurationBounds() {
		metricsDurationTask(t, db, fmt.Sprintf("edge-%d", i), bound, runtime.TaskCompleted)
		metricsDurationTask(t, db, fmt.Sprintf("above-%d", i), bound+time.Nanosecond, runtime.TaskCompleted)
		expectedSum += bound.Seconds() + (bound + time.Nanosecond).Seconds()
	}
	metricsDurationTask(t, db, "backward", -time.Second, runtime.TaskFailed)
	if _, err := db.db.Exec(`INSERT INTO task_heads VALUES('legacy','private-session',2,'canceled')`); err != nil {
		t.Fatal(err)
	}
	s, err := db.Metrics(ctx)
	if err != nil || s.Validate() != nil || s.TaskDuration == nil {
		t.Fatal(s, err)
	}
	d := s.TaskDuration
	if d.Groups[0].Count != 21 || math.Abs(d.Groups[0].SumSeconds-expectedSum) > 1e-8 || d.Groups[1].Count != 0 || d.Groups[1].Unavailable[1].Value != 1 || d.Groups[2].Unavailable[0].Value != 1 {
		t.Fatal(d)
	}
	for i, count := range d.Groups[0].BucketCounts {
		want := int64(2)
		if i == 10 {
			want = 1
		}
		if count != want {
			t.Fatalf("bucket%d=%d want%d", i, count, want)
		}
	}
	body, err := json.Marshal(s)
	if err != nil || strings.Contains(string(body), "private") || strings.Contains(string(body), "edge-") {
		t.Fatal("timing metadata leaked", err)
	}
}

func TestMetricsDurationDoesNotReadJournalPayloads(t *testing.T) {
	db, _ := submissionStore(t)
	metricsDurationTask(t, db, "private-task", 150*time.Millisecond, runtime.TaskCompleted)
	before, err := db.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Keep JSON valid for the kind index, but make timestamps unusable and add
	// a large private payload. Read metrics from the already committed projection.
	if _, err := db.db.Exec(`UPDATE events SET body=json_set(body,'$.time','private-not-a-time','$.data.text',hex(zeroblob(1000000)))`); err != nil {
		t.Fatal(err)
	}
	after, err := db.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(before.TaskDuration)
	y, _ := json.Marshal(after.TaskDuration)
	if string(x) != string(y) {
		t.Fatal("timings reinterpreted raw journal payloads")
	}
}

func TestMetricsDurationRejectsProjectionAndEpochCorruption(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE task_timings SET terminal_event_id='other'`,
		`UPDATE task_timings SET terminal_sequence=3`,
		`UPDATE task_timings SET state='failed'`,
		`UPDATE task_timings SET duration_ns=1.5`,
		`UPDATE task_timings SET started_at='private-invalid'`,
		`UPDATE task_timings SET started_at='1970-01-01T01:01:40+01:00'`,
		`UPDATE task_timing_metadata SET started_at='private-invalid'`,
		`UPDATE task_timing_metadata SET started_at='2500-01-01T00:00:00Z'`,
		`DELETE FROM task_timing_metadata`,
	} {
		t.Run(mutation, func(t *testing.T) {
			db, _ := submissionStore(t)
			metricsDurationTask(t, db, "task", time.Second, runtime.TaskCompleted)
			if _, err := db.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Metrics(context.Background()); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid projection released", err)
			}
		})
	}
}
