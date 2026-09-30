package telemetry

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// BenchmarkMetricsCompletedTasks measures the actual read-only snapshot path.
// Setup uses a single owned transaction, not runtime task execution. All tasks
// have canonical journal pairs and consistent 100ms terminal timing metadata.
func BenchmarkMetricsCompletedTasks(b *testing.B) {
	for _, size := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("tasks_%d", size), func(b *testing.B) {
			b.StopTimer()
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "metrics.db")
			s, err := Open(ctx, path)
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer tx.Rollback()
			head, err := tx.PrepareContext(ctx, `INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES(?,?,2,'completed')`)
			if err != nil {
				b.Fatal(err)
			}
			defer head.Close()
			events, err := tx.PrepareContext(ctx, `INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,?,?)`)
			if err != nil {
				b.Fatal(err)
			}
			defer events.Close()
			timing, err := tx.PrepareContext(ctx, `INSERT INTO task_timings(task_id,started_at,terminal_event_id,terminal_sequence,state,duration_ns,reason) VALUES(?,?,?,2,'completed',100000000,'observed')`)
			if err != nil {
				b.Fatal(err)
			}
			defer timing.Close()
			base := time.Now().UTC().Add(-time.Duration(size+1) * time.Second)
			for i := 0; i < size; i++ {
				at := base.Add(time.Duration(i) * time.Second)
				id := fmt.Sprintf("benchmark-task-%06d", i)
				start := runtime.Event{Version: 1, ID: id + "-start", TaskID: id, SessionID: id, CorrelationID: id, Sequence: 1, Kind: runtime.TaskStarted, Time: at}
				end := start
				end.ID = id + "-end"
				end.Sequence = 2
				end.Kind = runtime.TaskCompleted
				end.Time = at.Add(100 * time.Millisecond)
				end.CausationID = start.ID
				if _, err := head.ExecContext(ctx, id, id); err != nil {
					b.Fatal(err)
				}
				for _, event := range []runtime.Event{start, end} {
					body, err := event.Encode()
					if err != nil {
						b.Fatal(err)
					}
					if _, err := events.ExecContext(ctx, event.ID, id, event.Sequence, body); err != nil {
						b.Fatal(err)
					}
				}
				if _, err := timing.ExecContext(ctx, id, at.Format(time.RFC3339Nano), end.ID); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			if err := s.Close(); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			reader, err := OpenReadOnly(ctx, path)
			if err != nil {
				b.Fatal(err)
			}
			defer reader.Close()
			snapshot, err := reader.Metrics(ctx)
			if err != nil || snapshot.Validate() != nil || snapshot.TaskDuration == nil {
				b.Fatal("snapshot qualification", err)
			}
			g := snapshot.TaskDuration.Groups[0]
			if snapshot.Groups[0].Counts[1].Value != int64(size) || g.Count != int64(size) || g.BucketCounts[0] != int64(size) || math.Abs(g.SumSeconds-float64(size)*.1) > float64(size)*1e-10 {
				b.Fatal("incorrect benchmark projection")
			}
			if snapshot.Groups[1].Name != "runtime_events" || snapshot.Groups[1].Counts[0].Value != int64(size) || snapshot.Groups[1].Counts[1].Value != int64(size) || snapshot.Groups[2].Name != "runtime_operations" {
				b.Fatal("incorrect benchmark event projection")
			}
			for _, count := range snapshot.Groups[2].Counts {
				if count.Value != 0 {
					b.Fatal("unexpected benchmark operation", count)
				}
			}
			for _, count := range g.BucketCounts[1:] {
				if count != 0 {
					b.Fatal("unexpected timing bucket")
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				if _, err := reader.Metrics(ctx); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(info.Size()), "database_bytes")
		})
	}
}
