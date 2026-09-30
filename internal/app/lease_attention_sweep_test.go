package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func TestDispatcherLeaseAttentionSweepPersistsWithoutReleasing(t *testing.T) {
	svc, db, calls := recoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Supply a historical fixture clock to the normal acquisition path: this
	// creates an already-expired lease without sleeps or direct SQL mutation.
	at := time.Now().Add(-time.Minute).UTC()
	start := runtime.Event{Version: 1, ID: "attention-task-start", TaskID: "attention-task", SessionID: "attention-session", CorrelationID: "attention-task", Sequence: 1, Time: at, Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	lease, err := db.AcquireLease(ctx, start.TaskID, "attention-owner", "workspace", true, at, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, start.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	options := workers.LeaseAttentionOptions{State: "open", Limit: 100}
	empty, err := db.ListLeaseAttention(ctx, options)
	if err != nil || len(empty.Items) != 0 {
		t.Fatal("fixture already observed", err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var observed workers.LeaseAttentionPage
	for {
		observed, err = db.ListLeaseAttention(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		if len(observed.Items) == 1 && dispatcher.Health().Status == "healthy" {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("live dispatcher did not produce healthy attention observation", dispatcher.Health())
		case <-dispatcher.done:
			t.Fatal("dispatcher exited before observation")
		}
	}
	item := observed.Items[0]
	if observed.Validate() != nil || item.TaskID != start.TaskID || !item.Writer || item.State != "open" || item.Reason != "expired_unreleased" || !item.LeaseExpires.Equal(lease.Expires) || item.ID == lease.Token {
		t.Fatal("wrong attention projection", observed)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal("open attention degraded dispatcher", err)
	}
	leases, err := db.InspectLeases(ctx, "workspace")
	if err != nil || len(leases) != 1 || leases[0] != lease {
		t.Fatal("attention released or changed writer", err)
	}
	after, err := db.Read(ctx, start.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("attention changed task journal", err)
	}
	if calls.Load() != 0 {
		t.Fatal("attention dispatched a model")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A fresh read-only app inspection proves the same durable record survives
	// shutdown and does not invent another attention ID on observation.
	reopened, err := InspectLeaseAttention(ctx, svc.settings.Telemetry.Database, options)
	if err != nil || !reflect.DeepEqual(reopened, observed) {
		t.Fatal("attention did not persist through restart-safe inspection", err)
	}
	if calls.Load() != 0 {
		t.Fatal("read-only inspection dispatched a model")
	}
}
