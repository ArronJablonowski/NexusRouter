package workers_test

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestSupervisorUsesValidatedHostWorkerIdentity(t *testing.T) {
	store, supervisor := setup(t)
	w := work("fixed-worker-task")
	w.WorkerID = "workboard-worker-capability"
	if output, err := supervisor.Run(context.Background(), w); err != nil || output != "answer" {
		t.Fatal(output, err)
	}
	events, err := store.Read(context.Background(), w.TaskID, 0, 100)
	if err != nil || len(events) == 0 {
		t.Fatal(events, err)
	}
	for _, event := range events {
		if event.WorkerID != w.WorkerID {
			t.Fatalf("host identity was replaced: %+v", event)
		}
	}

	invalid := work("invalid-worker-task")
	invalid.WorkerID = "worker identity with spaces"
	if output, err := supervisor.Run(context.Background(), invalid); err != workers.ErrWork || output != "" {
		t.Fatalf("invalid identity output=%q err=%v", output, err)
	}
	if events, err = store.Read(context.Background(), invalid.TaskID, 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("invalid identity reached durable execution: %+v err=%v", events, err)
	}
}
