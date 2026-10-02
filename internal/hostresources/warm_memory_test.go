package hostresources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestWarmChargesRemainDurableAndContendAcrossStores(t *testing.T) {
	a, b, _ := fixture(t, 2)
	now := time.Now().UTC()
	ctx := context.Background()
	s := resources.Snapshot{Time: now, TotalRAM: 128 << 30, AvailableRAM: 60 << 30, CPUs: 8, UnifiedMemory: true}
	r := request(t, "warm-first", now)
	r.RAMBytes = 64 << 30
	if _, err := a.Acquire(ctx, s, r, now); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("cold load unexpectedly admitted", err)
	}
	r.ColdRAMBytes = r.RAMBytes
	r.RAMBytes = 32 << 30
	r.ResidencyDigest = r.ConfigDigest
	first, err := a.Acquire(ctx, s, r, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.Acquire(ctx, s, r, now)
	if err != nil || again.Request != first.Request || again.Request.ColdRAMBytes != 64<<30 {
		t.Fatal("durable warm binding changed", again, err)
	}
	drift := r
	drift.ColdRAMBytes++
	if _, err := b.Acquire(ctx, s, drift, now); !errors.Is(err, resources.ErrReservationConflict) {
		t.Fatal("cold identity drift accepted", err)
	}
	second := r
	second.ReservationID = "warm-second"
	second.TaskID = "task-second"
	second.Owner.DaemonID = "daemon-b"
	if _, err := b.Acquire(ctx, s, second, now); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("warm charge bypassed aggregate RAM", err)
	}
	if _, err := a.Release(ctx, r.ReservationID, r.Owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Acquire(ctx, s, second, now); err != nil {
		t.Fatal("released capacity not reusable", err)
	}
}
