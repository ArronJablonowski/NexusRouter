package resources

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestPlatformReserveParallelModelsAndPlanner(t *testing.T) {
	now := time.Now().UTC()
	s := Snapshot{Time: now, CPUs: 20, TotalRAM: 128 << 30, AvailableRAM: 88 << 30, UnifiedMemory: true, RAMReserveBytes: SparkRAMReserveBytes}
	b, _ := NewBudget(Limits{MaxConcurrent: 4, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
	first, err := b.Reserve(s, Need{RAM: 48 << 30}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := b.Reserve(s, Need{RAM: 32 << 30}, now)
	if err != nil {
		t.Fatal("second model should fit exactly above buffer", err)
	}
	plan, err := b.Plan(context.Background(), CapacityRequest{Version: 1, Snapshot: s, Need: Need{RAM: 1}, Now: now})
	if err != nil || plan.Headroom.RAMBytes != 0 || plan.MaxAdditional != 0 {
		t.Fatal("planner offered reserve", plan, err)
	}
	if r, err := b.Reserve(s, Need{RAM: 1}, now); !errors.Is(err, ErrCapacity) || r != nil {
		t.Fatal("consumed free buffer", err)
	}
	if low, err := b.LowMemory(s, Need{RAM: 1}, now); err != nil || !low {
		t.Fatal("residency pressure ignored reserve", low, err)
	}
	second()
	second()
	plan, err = b.Plan(context.Background(), CapacityRequest{Version: 1, Snapshot: s, Need: Need{RAM: 32 << 30}, Now: now})
	if err != nil || plan.Headroom.RAMBytes != 32<<30 || plan.MaxAdditional != 1 {
		t.Fatal("release/planner disagreement", plan, err)
	}
}

func TestRAMReserveBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		total, available, reserved, floor uint64
		pct                               float64
		want                              uint64
		ok                                bool
	}{
		{"exact", 128, 88, 72, 8, 100, 8, true},
		{"percentage stricter", 100, 100, 0, 8, 80, 80, true},
		{"buffer stricter", 100, 100, 0, 8, 99, 92, true},
		{"below floor", 100, 7, 0, 8, 100, 0, false},
		{"at floor", 100, 8, 0, 8, 100, 0, true},
		{"reservation exceeds", 100, 9, 2, 8, 100, 0, false},
		{"tiny container", 4, 4, 0, 8, 100, 0, false},
		{"overflow", math.MaxUint64, math.MaxUint64, math.MaxUint64, 8, 100, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ramHeadroom(Snapshot{TotalRAM: tc.total, AvailableRAM: tc.available, RAMReserveBytes: tc.floor}, tc.reserved, tc.pct)
			if got != tc.want || ok != tc.ok {
				t.Fatal(got, ok)
			}
		})
	}
}
