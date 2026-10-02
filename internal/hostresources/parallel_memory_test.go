package hostresources

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"testing"
	"time"
)

func TestParallelModelsShareSparkBufferAcrossDaemons(t *testing.T) {
	a, b, _ := fixture(t, 4)
	now := time.Now().UTC()
	ctx := context.Background()
	s := resources.Snapshot{Time: now, CPUs: 20, TotalRAM: 128 << 30, AvailableRAM: 88 << 30, UnifiedMemory: true, RAMReserveBytes: resources.SparkRAMReserveBytes}
	first := request(t, "large-model", now)
	first.ModelID = "large"
	first.RAMBytes = 48 << 30
	second := request(t, "small-model", now)
	second.ModelID = "small"
	second.RAMBytes = 32 << 30
	second.Owner.DaemonID = "daemon-b"
	if _, err := a.Acquire(ctx, s, first, now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Acquire(ctx, s, second, now); err != nil {
		t.Fatal("independent second model denied", err)
	}
	third := request(t, "exceeds-buffer", now)
	third.RAMBytes = 1
	if _, err := a.Acquire(ctx, s, third, now); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("buffer spent across stores", err)
	}
	if _, err := b.Release(ctx, second.ReservationID, second.Owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(ctx, s, third, now); err != nil {
		t.Fatal("capacity not released", err)
	}
}
