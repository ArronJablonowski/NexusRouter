package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestTraceSnapshotIncludesMidTaskContextCompaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "trace-compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, _ := contextCompactionFixture(t, store, "trace-compaction")
	if err := store.Append(ctx, activation.Sequence-1, activation); err != nil {
		t.Fatal(err)
	}
	turn := runtime.Event{Version: 1, ID: "trace-next-turn", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: activation.Sequence + 1, Time: activation.Time.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "next-turn", AttemptID: "next-attempt"}
	if err := store.Append(ctx, turn.Sequence-1, turn); err != nil {
		t.Fatal(err)
	}
	done := turn
	done.ID, done.Sequence, done.Time, done.Kind = "trace-next-done", turn.Sequence+1, turn.Time.Add(time.Second), runtime.TurnCompleted
	done.Data.Text, done.Data.FinishReason = "done", "stop"
	if err := store.Append(ctx, done.Sequence-1, done); err != nil {
		t.Fatal(err)
	}
	terminal := runtime.Event{Version: 1, ID: "trace-task-done", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: done.Sequence + 1, Time: done.Time.Add(time.Second), Kind: runtime.TaskCompleted, TurnID: turn.TurnID, AttemptID: turn.AttemptID}
	if err := store.Append(ctx, terminal.Sequence-1, terminal); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Traces(ctx, 32)
	if err != nil || snapshot.Validate() != nil || len(snapshot.Traces) != 2 {
		t.Fatal(snapshot, err)
	}
	found := false
	for _, trace := range snapshot.Traces {
		for _, span := range trace.Spans {
			found = found || span.Name == "compaction" && span.Outcome == "applied" && span.StartedAt.Equal(activation.Time)
		}
	}
	if !found {
		t.Fatal("mid-task compaction trace instant missing", snapshot)
	}
}
