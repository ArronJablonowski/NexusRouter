package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestDurationMetricsConcurrentWriterAndReadOnlySnapshots(t *testing.T) {
	s, path := generationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	done := make(chan error, 1)
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	go func() {
		at := time.Now().UTC().Add(-time.Second)
		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("completed-%02d", i)
			for _, e := range []runtime.Event{
				timingEvent(id, 1, runtime.TaskStarted, at),
				timingEvent(id, 2, runtime.TaskCompleted, at.Add(100*time.Millisecond)),
				timingEvent(fmt.Sprintf("pending-%02d", i), 1, runtime.TaskStarted, at),
			} {
				if err := s.Append(ctx, e.Sequence-1, e); err != nil {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()
	var previousCompleted, previousTotal int64
	check := func(snapshot metrics.Snapshot) {
		t.Helper()
		if snapshot.Validate() != nil || snapshot.TaskDuration == nil {
			t.Fatal("incoherent snapshot", snapshot)
		}
		var total int64
		for _, c := range snapshot.Groups[0].Counts {
			total += c.Value
		}
		completed := snapshot.Groups[0].Counts[1].Value
		duration := snapshot.TaskDuration.Groups[0]
		if completed < previousCompleted || total < previousTotal || duration.Count != completed || duration.BucketCounts[0] != completed || math.Abs(duration.SumSeconds-float64(completed)*.1) > 1e-9 || duration.Unavailable[0].Value != 0 || duration.Unavailable[1].Value != 0 {
			t.Fatal("lifecycle and duration separated across snapshots", completed, total, duration)
		}
		previousCompleted, previousTotal = completed, total
	}
	for !joined {
		snapshot, err := ro.Metrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		check(snapshot)
		select {
		case err := <-done:
			joined = true
			if err != nil {
				t.Fatal(err)
			}
		default:
		}
	}
	final, err := ro.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(final)
	if final.Groups[0].Counts[0].Value != 20 || previousCompleted != 20 || previousTotal != 40 {
		t.Fatal("writer not fully observed", final)
	}
}

func TestDurationMetricsPinnedWALSnapshotSurvivesTerminalCommit(t *testing.T) {
	s, path := generationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	at := time.Now().UTC().Add(-time.Second)
	if err := s.Append(ctx, 0, timingEvent("task", 1, runtime.TaskStarted, at)); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	tx, err := ro.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	old := metrics.NewSnapshot(29, time.Now().UTC())
	// This first lifecycle read establishes the WAL snapshot before the writer
	// atomically appends a terminal event, head update and duration projection.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM task_heads WHERE state='running'`).Scan(&old.Groups[0].Counts[0].Value); err != nil {
		t.Fatal(err)
	}
	if old.Groups[0].Counts[0].Value != 1 {
		t.Fatal("fixture was not pending")
	}
	if err := s.Append(ctx, 1, timingEvent("task", 2, runtime.TaskCompleted, at.Add(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	if err := readTaskDuration(ctx, tx, &old); err != nil || old.Validate() != nil {
		t.Fatal("old snapshot mixed new timing", old, err)
	}
	if old.TaskDuration.Groups[0].Count != 0 {
		t.Fatal("terminal leaked into pinned reader", old)
	}
	newer, err := s.Metrics(ctx)
	if err != nil || newer.Validate() != nil {
		t.Fatal(newer, err)
	}
	if newer.Groups[0].Counts[0].Value != 0 || newer.Groups[0].Counts[1].Value != 1 || newer.TaskDuration.Groups[0].Count != 1 || newer.TaskDuration.Groups[0].BucketCounts[0] != 1 || newer.TaskDuration.Groups[0].SumSeconds != .1 || !newer.TaskDuration.StartedAt.Equal(old.TaskDuration.StartedAt) {
		t.Fatal("fresh snapshot did not observe atomic terminal", newer)
	}
	// Re-reading the same transaction remains exactly old even after the fresh
	// connection has observed the writer's commit.
	again := metrics.NewSnapshot(29, old.ObservedAt)
	again.Groups[0].Counts[0].Value = 1
	if err := readTaskDuration(ctx, tx, &again); err != nil || !reflect.DeepEqual(old, again) {
		t.Fatal("snapshot moved", again, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := readTaskDuration(canceled, tx, &again); err == nil {
		t.Fatal("canceled duration read succeeded")
	}
}
