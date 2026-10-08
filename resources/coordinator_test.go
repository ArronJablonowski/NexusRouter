package resources

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func coordinatorFixture(t *testing.T, slots int) (*InMemoryCoordinator, Snapshot, time.Time) {
	t.Helper()
	now := time.Unix(1000, 0).UTC()
	budget, err := NewBudget(Limits{MaxConcurrent: slots, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewInMemoryCoordinator(budget)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Time: now, CPUs: 8, TotalRAM: 8 << 30, AvailableRAM: 8 << 30}
	return coordinator, snapshot, now
}

func coordinatorRequest(now time.Time, id string) ReservationRequest {
	request := validReservationRequest(now)
	request.ReservationID = id
	request.GPUDevice = ""
	request.VRAMBytes = 0
	request.RAMBytes = 2 << 30
	request.TTL = 2 * time.Second
	return request
}

func TestInMemoryCoordinatorDuplicateAndConflict(t *testing.T) {
	coordinator, snapshot, now := coordinatorFixture(t, 1)
	request := coordinatorRequest(now, "same-id")
	first, err := coordinator.Acquire(context.Background(), snapshot, request, now)
	if err != nil || first.Validate() != nil {
		t.Fatal(first, err)
	}
	duplicate, err := coordinator.Acquire(context.Background(), snapshot, request, now)
	if err != nil || duplicate != first {
		t.Fatal("duplicate acquisition was not idempotent", duplicate, err)
	}
	conflict := request
	conflict.ContextTokens++
	if _, err = coordinator.Acquire(context.Background(), snapshot, conflict, now); !errors.Is(err, ErrReservationConflict) {
		t.Fatal("conflicting duplicate accepted", err)
	}
	other := coordinatorRequest(now, "other-id")
	if _, err = coordinator.Acquire(context.Background(), snapshot, other, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("duplicate consumed an extra slot or contention bypassed", err)
	}
}

func TestInMemoryCoordinatorOwnerRenewRelease(t *testing.T) {
	coordinator, snapshot, now := coordinatorFixture(t, 1)
	request := coordinatorRequest(now, "owned")
	binding, err := coordinator.Acquire(context.Background(), snapshot, request, now)
	if err != nil {
		t.Fatal(err)
	}
	other := ReservationOwner{ProcessID: "other-process", DaemonID: "other-daemon"}
	if _, err = coordinator.Renew(context.Background(), request.ReservationID, other, now.Add(time.Second), time.Second); !errors.Is(err, ErrReservationOwner) {
		t.Fatal("foreign renewal accepted", err)
	}
	if _, err = coordinator.Release(context.Background(), request.ReservationID, other, now.Add(time.Second)); !errors.Is(err, ErrReservationOwner) {
		t.Fatal("foreign release accepted", err)
	}
	renewed, err := coordinator.Renew(context.Background(), request.ReservationID, request.Owner, now.Add(time.Second), 3*time.Second)
	if err != nil || renewed.Validate() != nil || !renewed.ExpiresAt.Equal(now.Add(4*time.Second)) || !renewed.ExpiresAt.After(binding.ExpiresAt) {
		t.Fatal("renewal failed", renewed, err)
	}
	status, err := coordinator.Release(context.Background(), request.ReservationID, request.Owner, now.Add(1500*time.Millisecond))
	if err != nil || status.Validate() != nil || status.State != ReservationReleased {
		t.Fatal("release failed", status, err)
	}
	again, err := coordinator.Release(context.Background(), request.ReservationID, request.Owner, now.Add(1600*time.Millisecond))
	if err != nil || again.State != ReservationReleased {
		t.Fatal("release is not idempotent", again, err)
	}
	otherRequest := coordinatorRequest(now.Add(1600*time.Millisecond), "after-release")
	if _, err = coordinator.Acquire(context.Background(), snapshot, otherRequest, now.Add(1600*time.Millisecond)); err != nil {
		t.Fatal("released capacity remained occupied", err)
	}
}

func TestInMemoryCoordinatorMemoryAndConcurrentSlotContention(t *testing.T) {
	coordinator, snapshot, now := coordinatorFixture(t, 2)
	large := coordinatorRequest(now, "large")
	large.RAMBytes = 7 << 30
	if _, err := coordinator.Acquire(context.Background(), snapshot, large, now); err != nil {
		t.Fatal(err)
	}
	tooMuch := coordinatorRequest(now, "memory-denied")
	tooMuch.RAMBytes = 2 << 30
	if _, err := coordinator.Acquire(context.Background(), snapshot, tooMuch, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("memory contention bypassed", err)
	}

	parallel, parallelSnapshot, parallelNow := coordinatorFixture(t, 1)
	var admitted int
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for index := range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := coordinatorRequest(parallelNow, "parallel-"+string(rune('a'+index)))
			if _, err := parallel.Acquire(context.Background(), parallelSnapshot, request, parallelNow); err == nil {
				mutex.Lock()
				admitted++
				mutex.Unlock()
			}
		}()
	}
	wait.Wait()
	if admitted != 1 {
		t.Fatal("slot contention admitted", admitted)
	}
}

func TestInMemoryCoordinatorExpiryTombstoneAndSnapshot(t *testing.T) {
	coordinator, snapshot, now := coordinatorFixture(t, 1)
	request := coordinatorRequest(now, "expires")
	binding, err := coordinator.Acquire(context.Background(), snapshot, request, now)
	if err != nil {
		t.Fatal(err)
	}
	active, err := coordinator.Snapshot(context.Background(), now.Add(time.Second))
	if err != nil || active.Validate() != nil || active.Active != 1 || active.RAMBytes != request.RAMBytes || active.Released != 0 || active.Expired != 0 || len(active.DevicePools) != 0 {
		t.Fatal("invalid active snapshot", active, err)
	}
	expired, err := coordinator.Status(context.Background(), request.ReservationID, binding.ExpiresAt)
	if err != nil || expired.Validate() != nil || expired.State != ReservationExpired {
		t.Fatal("reservation did not expire", expired, err)
	}
	if _, err = coordinator.Renew(context.Background(), request.ReservationID, request.Owner, binding.ExpiresAt, time.Second); !errors.Is(err, ErrReservationExpired) {
		t.Fatal("expired reservation revived", err)
	}
	replayAt := binding.ExpiresAt.Add(8 * time.Second)
	duplicate, err := coordinator.Acquire(context.Background(), snapshot, request, replayAt)
	if !errors.Is(err, ErrReservationExpired) || duplicate != binding {
		t.Fatal("terminal tombstone returned an authorizing binding", duplicate, err)
	}
	next := coordinatorRequest(replayAt, "after-expiry")
	if _, err = coordinator.Acquire(context.Background(), snapshot, next, replayAt); !errors.Is(err, ErrCapacity) {
		t.Fatal("expired live reservation lost its capacity charge", err)
	}
	held, err := coordinator.Snapshot(context.Background(), replayAt)
	if err != nil || held.Active != 0 || held.Expired != 1 || held.RAMBytes != request.RAMBytes {
		t.Fatal("expired held capacity missing", held, err)
	}
	released, err := coordinator.Release(context.Background(), request.ReservationID, request.Owner, replayAt)
	if !errors.Is(err, ErrReservationExpired) || released.State != ReservationReleased {
		t.Fatal("expired owner could not release after execution returned", released, err)
	}
	if _, err = coordinator.Acquire(context.Background(), snapshot, next, replayAt); err != nil {
		t.Fatal("released capacity remained occupied", err)
	}
	final, err := coordinator.Snapshot(context.Background(), replayAt)
	if err != nil || final.Active != 1 || final.Expired != 0 || final.Released != 1 || final.RAMBytes != next.RAMBytes {
		t.Fatal("terminal snapshot mismatch", final, err)
	}
}

func TestInMemoryCoordinatorClockSkewAndCancellation(t *testing.T) {
	coordinator, snapshot, now := coordinatorFixture(t, 1)
	for name, requested := range map[string]time.Time{
		"future": now.Add(reservationClockSkew + time.Nanosecond),
		"past":   now.Add(-reservationClockSkew - time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			request := coordinatorRequest(requested, name)
			if _, err := coordinator.Acquire(context.Background(), snapshot, request, now); !errors.Is(err, ErrReservation) {
				t.Fatal("unsafe skew accepted", err)
			}
		})
	}
	boundary := coordinatorRequest(now.Add(reservationClockSkew), "boundary")
	binding, err := coordinator.Acquire(context.Background(), snapshot, boundary, now)
	if err != nil {
		t.Fatal("bounded skew rejected", err)
	}
	stale := binding.AcquiredAt.Add(-reservationClockSkew - time.Nanosecond)
	if _, err = coordinator.Renew(context.Background(), boundary.ReservationID, boundary.Owner, stale, time.Second); !errors.Is(err, ErrReservation) {
		t.Fatal("stale renewal accepted", err)
	}
	if _, err = coordinator.Release(context.Background(), boundary.ReservationID, boundary.Owner, stale); !errors.Is(err, ErrReservation) {
		t.Fatal("stale release accepted", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinator.Snapshot(canceled, now); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrReservation) {
		t.Fatal("cancellation not preserved", err)
	}
}

func TestInMemoryCoordinatorAnonymizesDevicePools(t *testing.T) {
	coordinator, snapshot, now := coordinatorFixture(t, 2)
	total := uint64(8 << 30)
	snapshot.GPUs = &GPUInventory{Time: now, Sources: []GPUObservation{{Source: "nvidia-smi", Status: "observed", Devices: []GPUDevice{
		{ID: "GPU-abcdef01", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: total, AvailableBytes: total},
		{ID: "GPU-abcdef02", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: total, AvailableBytes: total},
	}}}}
	for index, device := range []string{"nvidia:GPU-abcdef01", "nvidia:GPU-abcdef02"} {
		request := coordinatorRequest(now, "device-"+string(rune('a'+index)))
		request.GPUDevice, request.VRAMBytes = device, uint64(index+1)<<30
		if _, err := coordinator.Acquire(context.Background(), snapshot, request, now); err != nil {
			t.Fatal(err)
		}
	}
	status, err := coordinator.Snapshot(context.Background(), now)
	if err != nil || status.Validate() != nil || len(status.DevicePools) != 2 || status.DevicePools[0].VRAMBytes != 1<<30 || status.DevicePools[1].VRAMBytes != 2<<30 {
		t.Fatal(status, err)
	}
	body, err := json.Marshal(status)
	if err != nil || strings.Contains(strings.ToLower(string(body)), "gpu-") || strings.Contains(string(body), "abcdef") {
		t.Fatal("device identity leaked", string(body), err)
	}
}
