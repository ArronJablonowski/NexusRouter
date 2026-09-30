package workers_test

import (
	"context"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

type originJournal func(context.Context, int64, runtime.Event) error

func (j originJournal) Append(ctx context.Context, seq int64, e runtime.Event) error {
	return j(ctx, seq, e)
}

func TestWorkerPersistsOwnedDelegationOrigin(t *testing.T) {
	db, _ := setup(t)
	index := 0
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate_batch", BatchIndex: &index}
	w := work("origin-child")
	w.DelegationOrigin = origin
	journal := originJournal(func(ctx context.Context, seq int64, e runtime.Event) error {
		if e.Kind == runtime.TaskStarted {
			// Mutate caller storage at the first dispatch boundary. Persisted
			// metadata must already own its independent pointer and fields.
			index = 3
			origin.ToolCallID = "caller changed"
			if e.Data.DelegationOrigin == nil || *e.Data.DelegationOrigin.BatchIndex != 0 || e.Data.DelegationOrigin.ToolCallID != "call" {
				t.Error("supervisor borrowed origin storage")
			}
		} else if e.Data.DelegationOrigin != nil {
			t.Error("origin persisted outside start")
		}
		return db.Append(ctx, seq, e)
	})
	sup, err := workers.New(1, time.Millisecond, time.Second, db, journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	events, err := db.Read(context.Background(), w.TaskID, 0, 100)
	if err != nil || len(events) == 0 {
		t.Fatal(err)
	}
	saved := events[0].Data.DelegationOrigin
	if saved == nil || saved.Validate() != nil || saved.ToolCallID != "call" || *saved.BatchIndex != 0 {
		t.Fatal("origin did not survive durable roundtrip")
	}
}

func TestWorkerRejectsInvalidOriginBeforePersistenceOrExecution(t *testing.T) {
	db, _ := setup(t)
	appends, calls := 0, 0
	sup, err := workers.New(1, time.Millisecond, time.Second, db, originJournal(func(context.Context, int64, runtime.Event) error { appends++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	w := work("bad-origin")
	w.DelegationOrigin = &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate_batch"}
	w.Execute = func(context.Context) (string, error) { calls++; return "answer", nil }
	if _, err := sup.Run(context.Background(), w); err != workers.ErrWork || appends != 0 || calls != 0 {
		t.Fatal("invalid origin crossed dispatch boundary", err, appends, calls)
	}
}
