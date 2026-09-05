package resources

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestAdaptiveHeadroomTiers(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		room                  uint64
		cpus, ceiling, active int
		allowed               bool
	}{
		{"below16", 16<<30 - 1, 64, 64, 1, false}, {"at16", 16 << 30, 64, 64, 1, true}, {"at64", 64 << 30, 64, 64, 2, false}, {"above64", 64<<30 + 1, 64, 64, 2, true},
		{"cpuunknown", 128 << 30, 0, 64, 1, false}, {"cpunegative", 128 << 30, -1, 64, 1, false}, {"cpucap", 128 << 30, 2, 64, 2, false}, {"configuredcap", 128 << 30, 64, 3, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewAdaptiveBudget(Limits{tc.ceiling, 100, 100, time.Second})
			if err != nil {
				t.Fatal(err)
			}
			b.active = tc.active
			now := time.Now()
			release, err := b.Reserve(Snapshot{Time: now, TotalRAM: tc.room, AvailableRAM: tc.room, CPUs: tc.cpus}, Need{RAM: 1}, now)
			if (err == nil) != tc.allowed {
				t.Fatalf("admission %v", err)
			}
			if release != nil {
				release()
			}
		})
	}
}

func TestAdaptivePressureReservationsAndFixedOverride(t *testing.T) {
	now := time.Now()
	snapshot := Snapshot{Time: now, TotalRAM: 128 << 30, AvailableRAM: 128 << 30, CPUs: 8}
	b, _ := NewAdaptiveBudget(Limits{8, 100, 100, time.Second})
	first, err := b.Reserve(snapshot, Need{RAM: 64<<30 + 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Reserve(snapshot, Need{RAM: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Reserve(snapshot, Need{RAM: 1}, now); err == nil {
		t.Fatal("live reservations ignored")
	}
	snapshot.AvailableRAM = 64<<30 + 2
	if _, err = b.Reserve(snapshot, Need{RAM: 1}, now); err == nil {
		t.Fatal("fresh pressure ignored")
	}
	first()
	first()
	second()
	if b.active != 0 || b.used.RAM != 0 {
		t.Fatal("release leaked")
	}
	snapshot.AvailableRAM = 128 << 30
	release, err := b.Reserve(snapshot, Need{RAM: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	release()
	fixed, _ := NewBudget(Limits{4, 100, 100, time.Second})
	snapshot.TotalRAM = 1024
	snapshot.AvailableRAM = 1024
	snapshot.CPUs = 0
	for i := 0; i < 4; i++ {
		if _, err := fixed.Reserve(snapshot, Need{RAM: 1}, now); err != nil {
			t.Fatal("adaptive rules changed fixed budget")
		}
	}
}

func TestAdaptiveGPUAndUnified(t *testing.T) {
	now := time.Now()
	total := uint64(128 << 30)
	available := uint64(8 << 30)
	s := Snapshot{Time: now, TotalRAM: total, AvailableRAM: total, CPUs: 64, VRAMTotal: &total, VRAMAvailable: &available}
	b, _ := NewAdaptiveBudget(Limits{64, 100, 100, time.Second})
	release, err := b.Reserve(s, Need{RAM: 1, VRAM: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Reserve(s, Need{RAM: 1, VRAM: 1}, now); err == nil {
		t.Fatal("GPU headroom did not cap concurrency")
	}
	release()
	s.UnifiedMemory = true
	if _, err = b.Reserve(s, Need{RAM: 1, VRAM: 1}, now); err == nil {
		t.Fatal("unified double pool")
	}
	s.UnifiedMemory = false
	s.VRAMTotal = nil
	if _, err = b.Reserve(s, Need{RAM: 1, VRAM: 1}, now); err == nil {
		t.Fatal("unknown GPU")
	}
}

func TestByteCeilingExtremeAndConservative(t *testing.T) {
	max := uint64(math.MaxUint64)
	for _, tc := range []struct {
		total uint64
		pct   float64
		want  uint64
	}{{max, 100, max}, {max, 50, max / 2}, {max, 25, max / 4}, {100, 33.3, 33}, {101, 99, 99}, {1, math.SmallestNonzeroFloat64, 0}} {
		if got := byteCeiling(tc.total, tc.pct); got != tc.want {
			t.Fatalf("floor(%d * %g%%)=%d want %d", tc.total, tc.pct, got, tc.want)
		}
	}
	b, _ := NewBudget(Limits{1, 100, 100, time.Second})
	now := time.Now()
	release, err := b.Reserve(Snapshot{Time: now, TotalRAM: max, AvailableRAM: max}, Need{RAM: max}, now)
	if err != nil {
		t.Fatal(err)
	}
	release()
	b, _ = NewBudget(Limits{1, 50, 100, time.Second})
	if _, err = b.Reserve(Snapshot{Time: now, TotalRAM: max, AvailableRAM: max}, Need{RAM: max/2 + 1}, now); err == nil {
		t.Fatal("rounded upward")
	}
}

func TestAdaptiveConcurrentAdmissionRelease(t *testing.T) {
	b, _ := NewAdaptiveBudget(Limits{4, 100, 100, time.Second})
	now := time.Now()
	s := Snapshot{Time: now, TotalRAM: 128 << 30, AvailableRAM: 128 << 30, CPUs: 64}
	var wg sync.WaitGroup
	releases := make(chan func(), 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, err := b.Reserve(s, Need{RAM: 1}, now); err == nil {
				releases <- release
			}
		}()
	}
	wg.Wait()
	close(releases)
	if len(releases) != 4 {
		t.Fatalf("admitted %d", len(releases))
	}
	for release := range releases {
		release()
		release()
	}
	if b.active != 0 || b.used.RAM != 0 {
		t.Fatal("leaked reservations")
	}
}
