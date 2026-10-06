package resources

import (
	"testing"
	"time"
)

func TestBackendManagedMemoryRetainsGuards(t *testing.T) {
	now := time.Now()
	swap := uint64(0)
	thermal := false
	snapshot := Snapshot{Time: now, TotalRAM: 24 << 30, AvailableRAM: 1 << 30, UnifiedMemory: true, Source: "darwin-vm-stat-estimate", SwapUsed: &swap, ThermalPressure: &thermal}
	limits := Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 85, MaxAge: time.Second}
	b, _ := NewBudget(limits)
	if _, err := b.Reserve(snapshot, Need{RAM: 10 << 30}, now); err == nil {
		t.Fatal("strict admission bypassed")
	}
	release, err := b.Reserve(snapshot, Need{RAM: 10 << 30, BackendManagedRAM: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(snapshot, Need{RAM: 1, BackendManagedRAM: true}, now); err == nil {
		t.Fatal("concurrency bypassed")
	}
	release()
	for _, kind := range []string{"linux", "swap_unknown", "thermal_unknown", "pressure", "oversized"} {
		s := snapshot
		n := Need{RAM: 10 << 30, BackendManagedRAM: true}
		switch kind {
		case "linux":
			s.Source = "linux"
		case "swap_unknown":
			s.SwapUsed = nil
		case "thermal_unknown":
			s.ThermalPressure = nil
		case "pressure":
			hot := true
			s.ThermalPressure = &hot
		case "oversized":
			n.RAM = 25 << 30
		}
		if _, err := b.Reserve(s, n, now); err == nil {
			t.Fatal(kind)
		}
	}
}
