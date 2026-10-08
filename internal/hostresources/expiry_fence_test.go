package hostresources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestExpiredLiveOwnerRetainsCapacityUntilRelease(t *testing.T) {
	a, b, _ := fixture(t, 1)
	ctx := context.Background()
	now := time.Unix(500, 0).UTC()
	first := request(t, "still-executing", now)
	if _, err := a.Acquire(ctx, snapshot(now), first, now); err != nil {
		t.Fatal(err)
	}
	at := now.Add(first.TTL)
	held, err := b.Snapshot(ctx, at)
	if err != nil || held.Active != 0 || held.Expired != 1 || held.RAMBytes != first.RAMBytes {
		t.Fatalf("expired outstanding charge not observable: %+v %v", held, err)
	}
	next := request(t, "replacement", at)
	next.Owner.DaemonID = "daemon-b"
	if _, err := b.Acquire(ctx, snapshot(at), next, at); !errors.Is(err, resources.ErrCapacity) {
		t.Fatalf("expired live execution lost capacity fence: %v", err)
	}
	if _, err := a.Renew(ctx, first.ReservationID, first.Owner, at, first.TTL); !errors.Is(err, resources.ErrReservationExpired) {
		t.Fatalf("expired execution renewed: %v", err)
	}
	wrong := first.Owner
	wrong.DaemonID = "daemon-b"
	if _, err := b.Release(ctx, first.ReservationID, wrong, at); !errors.Is(err, resources.ErrReservationOwner) {
		t.Fatalf("foreign owner released fence: %v", err)
	}
	if _, err := b.Recover(ctx, first.ReservationID, at); !errors.Is(err, ErrRecovery) {
		t.Fatalf("live owner recovered: %v", err)
	}
	if status, err := a.Release(ctx, first.ReservationID, first.Owner, at); !errors.Is(err, resources.ErrReservationExpired) || status.State != resources.ReservationReleased {
		t.Fatalf("returned execution could not release: %+v %v", status, err)
	}
	if _, err := b.Acquire(ctx, snapshot(at), next, at); err != nil {
		t.Fatalf("released capacity unavailable: %v", err)
	}
	if _, err := a.Acquire(ctx, snapshot(at), first, at); !errors.Is(err, resources.ErrReservationExpired) {
		t.Fatalf("released identity replayed: %v", err)
	}
}
