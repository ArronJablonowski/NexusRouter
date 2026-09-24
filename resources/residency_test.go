package resources

import (
	"errors"
	"testing"
	"time"
)

func TestLowMemoryIncludesOutstandingReservations(t *testing.T) {
	now := time.Now()
	b, err := NewBudget(Limits{MaxConcurrent: 4, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Time: now, TotalRAM: 32 << 30, AvailableRAM: 32 << 30, CPUs: 8}
	n := Need{RAM: 20 << 30}
	if low, err := b.LowMemory(s, n, now); err != nil || low {
		t.Fatal("unexpected initial tier", low, err)
	}
	release, err := b.Reserve(s, n, now)
	if err != nil {
		t.Fatal(err)
	}
	if low, err := b.LowMemory(s, Need{RAM: 1}, now); err != nil || !low {
		t.Fatal("outstanding RAM reservation omitted", low, err)
	}
	release()
	if low, err := b.LowMemory(s, n, now); err != nil || low {
		t.Fatal("inspection mutated reservation accounting", low, err)
	}
	total, available := uint64(24<<30), uint64(24<<30)
	s.VRAMTotal, s.VRAMAvailable = &total, &available
	release, err = b.Reserve(s, Need{RAM: 1, VRAM: 10 << 30}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if low, err := b.LowMemory(s, Need{RAM: 1, VRAM: 1}, now); err != nil || !low {
		t.Fatal("outstanding VRAM reservation omitted", low, err)
	}
}

func TestLowMemoryBoundaryAndInvalidObservation(t *testing.T) {
	now := time.Now()
	b, _ := NewBudget(Limits{MaxConcurrent: 4, RAMPercent: 80, VRAMPercent: 85, MaxAge: time.Minute})
	s := Snapshot{Time: now, TotalRAM: 20 << 30, AvailableRAM: 20 << 30}
	if low, err := b.LowMemory(s, Need{RAM: 1}, now); err != nil || low {
		t.Fatal("16 GiB belongs to middle tier", low, err)
	}
	s.AvailableRAM--
	if low, err := b.LowMemory(s, Need{RAM: 1}, now); err != nil || !low {
		t.Fatal("below boundary not low", low, err)
	}
	s.AvailableRAM = 0
	if low, err := b.LowMemory(s, Need{RAM: 1}, now); err != nil || !low {
		t.Fatal("exhaustion must be low, not capacity credit", low, err)
	}
	s.Time = now.Add(-2 * time.Minute)
	if _, err := b.LowMemory(s, Need{RAM: 1}, now); !errors.Is(err, ErrResourceData) {
		t.Fatal("stale observation accepted", err)
	}
	s.Time = now
	hot := true
	s.ThermalPressure = &hot
	if _, err := b.LowMemory(s, Need{RAM: 1}, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("thermal pressure accepted", err)
	}
}

func TestLowMemoryUsesSelectedDeviceReservations(t *testing.T) {
	now := time.Now()
	b, _ := NewBudget(Limits{MaxConcurrent: 4, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
	s := deviceSnapshot(now)
	for i := range s.GPUs.Sources[0].Devices {
		s.GPUs.Sources[0].Devices[i].TotalBytes = 32 << 30
		s.GPUs.Sources[0].Devices[i].AvailableBytes = 32 << 30
	}
	release, err := b.Reserve(s, Need{RAM: 1, VRAM: 20 << 30, Device: "nvidia:GPU-abcdef01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for device, want := range map[string]bool{"nvidia:GPU-ABCDEF01": true, "nvidia:GPU-abcdef02": false} {
		if low, err := b.LowMemory(s, Need{RAM: 1, VRAM: 1, Device: device}, now); err != nil || low != want {
			t.Fatal("device accounting mixed or alias bypass", device, low, err)
		}
	}
	total := uint64(64 << 30)
	s.VRAMTotal, s.VRAMAvailable = &total, &total
	if _, err := b.LowMemory(s, Need{RAM: 1, VRAM: 1}, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("aggregate observation bypassed device reservations", err)
	}
}

func TestLowMemoryRejectsUnavailableExecutionSlots(t *testing.T) {
	for _, device := range []bool{false, true} {
		limits := Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute}
		now := time.Now()
		s := deviceSnapshot(now)
		s.TotalRAM, s.AvailableRAM, s.CPUs = 128<<30, 128<<30, 8
		need := Need{RAM: 1}
		budget, _ := NewBudget(limits)
		count := 1
		if device {
			limits.MaxConcurrent = 4
			budget, _ = NewAdaptiveBudget(limits)
			count = 2
			need.VRAM, need.Device = 1, "nvidia:GPU-abcdef01"
			for i := range s.GPUs.Sources[0].Devices {
				s.GPUs.Sources[0].Devices[i].TotalBytes = 64 << 30
				s.GPUs.Sources[0].Devices[i].AvailableBytes = 64 << 30
			}
		}
		for range count {
			release, err := budget.Reserve(s, need, now)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
		}
		if _, err := budget.LowMemory(s, need, now); !errors.Is(err, ErrCapacity) {
			t.Fatalf("device=%v: exhausted execution slot invited residency maintenance: %v", device, err)
		}
	}
}
