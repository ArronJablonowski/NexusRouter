package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestTaskSnapshotStatesAndReadOnlyRestart(t *testing.T) {
	for _, mode := range []string{"running", "interrupted", "completed", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			db, path := submissionStore(t)
			ctx := context.Background()
			start := event("start", 1, runtime.TaskStarted)
			start.Data.Messages = []providers.Message{{Role: "user", Content: "private prompt"}}
			events := []runtime.Event{start}
			turn := event("turn", 2, runtime.TurnStarted)
			turn.TurnID = "turn"
			turn.AttemptID = "attempt"
			if mode != "running" {
				events = append(events, turn)
			}
			if mode == "completed" || mode == "uncertain" {
				end := event("end", 3, runtime.TurnCompleted)
				end.TurnID = "turn"
				end.AttemptID = "attempt"
				end.Data.Text = "answer"
				if mode == "uncertain" {
					end.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "tool", Arguments: []byte(`{}`)}}
				}
				events = append(events, end)
				if mode == "completed" {
					events = append(events, event("complete", 4, runtime.TaskCompleted))
				} else {
					tool := event("tool", 4, runtime.ToolStarted)
					tool.TurnID = "turn"
					tool.AttemptID = "attempt"
					tool.Data.ToolCallID = "call"
					tool.Data.ToolName = "tool"
					tool.Data.Effect = runtime.NoEffect
					events = append(events, tool)
				}
			}
			for _, e := range events {
				if err := db.Append(ctx, e.Sequence-1, e); err != nil {
					t.Fatal(err)
				}
			}
			ro, err := OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			snapshot, err := ro.TaskSnapshot(ctx, "task")
			if err != nil || snapshot.Sequence != int64(len(events)) {
				t.Fatal(snapshot, err)
			}
			if snapshot.InterruptedTurn != (mode == "interrupted") || snapshot.UncertainEffects != (mode == "uncertain") {
				t.Fatal(snapshot)
			}
			if mode == "uncertain" && (len(snapshot.Pending) != 1 || !snapshot.Pending["call"].Dispatched) {
				t.Fatal(snapshot)
			}
			if mode == "completed" && (snapshot.State != "completed" || len(snapshot.Messages) != 2) {
				t.Fatal(snapshot)
			}
		})
	}
}

func TestTaskSnapshotRejectsCorruptionWithoutPartialPayload(t *testing.T) {
	for _, mode := range []string{"gap", "head", "session", "id", "body", "oversize", "count", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			appendValidity(t, db, validityEvents("task", true))
			query := ""
			switch mode {
			case "gap":
				query = `DELETE FROM events WHERE sequence=2`
			case "head":
				query = `UPDATE task_heads SET sequence=4`
			case "session":
				query = `UPDATE task_heads SET session_id='different'`
			case "id":
				query = `UPDATE events SET id='wrong' WHERE sequence=3`
			case "body":
				query = `UPDATE events SET body='{}' WHERE sequence=3`
			case "oversize":
				query = `UPDATE events SET body=json_object('padding',printf('%.*c',8388609,'x')) WHERE sequence=3`
			case "count":
				query = `UPDATE task_heads SET sequence=10001`
			case "unknown":
				query = `UPDATE task_heads SET state='unknown'`
			}
			if _, err := db.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			snapshot, err := db.TaskSnapshot(ctx, "task")
			if err == nil || !reflect.DeepEqual(snapshot, sessions.Snapshot{}) {
				t.Fatal("partial snapshot", snapshot, err)
			}
		})
	}
}

func TestTaskSnapshotMissingAndCanceled(t *testing.T) {
	db, _ := submissionStore(t)
	snapshot, err := db.TaskSnapshot(context.Background(), "missing")
	if !errors.Is(err, sql.ErrNoRows) || !reflect.DeepEqual(snapshot, sessions.Snapshot{}) {
		t.Fatal(snapshot, err)
	}
	if _, err = db.TaskSnapshot(context.Background(), strings.Repeat("x", 129)); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err = db.TaskSnapshot(ctx, "missing")
	if err == nil || !reflect.DeepEqual(snapshot, sessions.Snapshot{}) {
		t.Fatal(snapshot, err)
	}
}

func TestTaskSnapshotAggregateByteLimit(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TurnID, turn.AttemptID = "turn", "attempt"
	if err := db.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		delta := event(fmt.Sprintf("delta-%d", i), int64(i+3), runtime.ModelDelta)
		delta.TurnID, delta.AttemptID = "turn", "attempt"
		delta.Data.Text = strings.Repeat("x", 1<<20)
		if err := db.Append(ctx, delta.Sequence-1, delta); err != nil {
			t.Fatal(err)
		}
		if i == 6 {
			if snapshot, err := db.TaskSnapshot(ctx, "task"); err != nil || snapshot.Sequence != 9 {
				t.Fatal(snapshot.Sequence, err)
			}
		}
	}
	snapshot, err := db.TaskSnapshot(ctx, "task")
	if err == nil || !reflect.DeepEqual(snapshot, sessions.Snapshot{}) {
		t.Fatal("aggregate limit returned payload", err)
	}
}

func TestTaskSnapshotCoherentDuringConcurrentAppend(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	if err := db.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TurnID = "turn"
	turn.AttemptID = "attempt"
	if err := db.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for sequence := int64(3); sequence <= 52; sequence++ {
			delta := event(fmt.Sprintf("delta-%d", sequence), sequence, runtime.ModelDelta)
			delta.TurnID = "turn"
			delta.AttemptID = "attempt"
			delta.Data.Text = "partial"
			if err := db.Append(ctx, sequence-1, delta); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 20; i++ {
		snapshot, err := db.TaskSnapshot(ctx, "task")
		if err != nil || snapshot.State != "running" || snapshot.Sequence < 2 || snapshot.Sequence > 52 || !snapshot.InterruptedTurn {
			t.Fatal(snapshot, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TaskSnapshot(ctx, "task")
	if err != nil || snapshot.Sequence != 52 {
		t.Fatal(snapshot, err)
	}
}

func TestTaskSnapshotAllowsOpaqueEventIDAndCorrelation(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	start := event("event:start with space", 1, runtime.TaskStarted)
	start.CorrelationID = "worker:parent"
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TaskSnapshot(ctx, "task")
	if err != nil || snapshot.Sequence != 1 || snapshot.State != "running" {
		t.Fatal(snapshot, err)
	}
}
