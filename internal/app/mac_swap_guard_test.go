package app

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"testing"
	"time"
)

func TestMacSwapGrowthBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap uint64
		fail bool
	}{{"old swap", 9_000_000_000, false}, {"boundary", 14_000_000_000, false}, {"exceeded", 14_000_000_001, true}} {
		t.Run(tc.name, func(t *testing.T) {
			sampled := make(chan struct{}, 1)
			ctx, finish := watchSwapGrowth(context.Background(), 10_000_000_000, 4_000_000_000, time.Millisecond, func(context.Context) (resources.Snapshot, error) {
				select {
				case sampled <- struct{}{}:
				default:
				}
				return resources.Snapshot{SwapUsed: &tc.swap}, nil
			})
			defer finish()
			<-sampled
			if tc.fail {
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("did not cancel")
				}
				if !errors.Is(finish(), errSwapGrowth) {
					t.Fatal("missing cause")
				}
			} else {
				if err := finish(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestMacSwapProbeFailureStopsExecution(t *testing.T) {
	ctx, finish := watchSwapGrowth(context.Background(), 0, 4_000_000_000, time.Millisecond, func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{}, errors.New("unavailable")
	})
	defer finish()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("did not cancel")
	}
	if !errors.Is(finish(), ErrAdmission) {
		t.Fatal("missing failure")
	}
}

func TestSwapReservationPreservesCleanupError(t *testing.T) {
	cleanupErr := errors.New("durable release failed")
	calls := 0
	release := finishSwapReservation(func() error { return errSwapGrowth }, func() error { calls++; return cleanupErr })
	for i := 0; i < 2; i++ {
		err := release()
		if !errors.Is(err, errSwapGrowth) || !errors.Is(err, cleanupErr) {
			t.Fatalf("lost error: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("released %d times", calls)
	}
}
