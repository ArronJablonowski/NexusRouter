package hostresources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestWarmChargesRemainDurableAndContendAcrossStores(t *testing.T) {
	a, b, _ := fixture(t, 1)
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
	checks := 0
	verify := func(context.Context) (resources.Snapshot, time.Time, error) { checks++; return s, now, nil }
	if _, err := a.Acquire(ctx, s, r, now); !errors.Is(err, resources.ErrReservation) {
		t.Fatal("unfenced warm charge admitted", err)
	}
	first, err := a.AcquireVerifiedWarm(ctx, s, r, now, verify)
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.AcquireVerifiedWarm(ctx, s, r, now, verify)
	if err != nil || again.Request != first.Request || again.Request.ColdRAMBytes != 64<<30 {
		t.Fatal("durable warm binding changed", again, err)
	}
	drift := r
	drift.ColdRAMBytes++
	if _, err := b.AcquireVerifiedWarm(ctx, s, drift, now, verify); !errors.Is(err, resources.ErrReservationConflict) {
		t.Fatal("cold identity drift accepted", err)
	}
	second := r
	second.ReservationID = "warm-second"
	second.TaskID = "task-second"
	second.Owner.DaemonID = "daemon-b"
	if _, err := b.AcquireVerifiedWarm(ctx, s, second, now, verify); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("warm charge bypassed aggregate RAM", err)
	}
	if checks != 1 {
		t.Fatal("observed residency while peer active or replayed", checks)
	}
	if _, err := a.Release(ctx, r.ReservationID, r.Owner, now); err != nil {
		t.Fatal(err)
	}
	s.AvailableRAM = 16 << 30
	if _, err := b.AcquireVerifiedWarm(ctx, s, second, now, verify); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("warm budget bypassed measured RAM", err)
	}
	s.AvailableRAM = 60 << 30
	if _, err := b.AcquireVerifiedWarm(ctx, s, second, now, verify); err != nil {
		t.Fatal("released capacity not reusable", err)
	}
}

func TestWarmVerificationHoldsCrossProcessAdmissionFence(t *testing.T) {
	a, b, _ := fixture(t, 1)
	ctx := context.Background()
	now := time.Now().UTC()
	s := snapshot(now)
	r := request(t, "warm-fenced", now)
	r.ColdRAMBytes = 20
	r.ResidencyDigest = r.ConfigDigest
	entered, proceed := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := a.AcquireVerifiedWarm(ctx, s, r, now, func(context.Context) (resources.Snapshot, time.Time, error) {
			close(entered)
			<-proceed
			return s, now, nil
		})
		first <- err
	}()
	<-entered
	second := make(chan error, 1)
	coldRequest := request(t, "cold-racer", now)
	go func() { _, err := b.Acquire(ctx, s, coldRequest, now); second <- err }()
	select {
	case err := <-second:
		close(proceed)
		<-first
		t.Fatal("cold admission crossed residency fence", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("racer was not excluded", err)
	}
	// An expired lease is not permission to reuse a potentially active model.
	later := now.Add(2 * time.Second)
	s.Time = later
	r2 := r
	r2.ReservationID = "after-expiry"
	r2.RequestedAt = later
	if _, err := b.AcquireVerifiedWarm(ctx, s, r2, later, func(context.Context) (resources.Snapshot, time.Time, error) {
		t.Error("expired owner allowed observation")
		return s, later, nil
	}); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("expiry incorrectly granted warm authority", err)
	}
}
