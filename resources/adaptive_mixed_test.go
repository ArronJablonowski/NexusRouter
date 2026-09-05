package resources

import (
	"testing"
	"time"
)

func TestAdaptiveMixedRAMAndDiscreteGPURequests(t *testing.T) {
	now := time.Now()
	vram := uint64(8 << 30)
	snapshot := Snapshot{Time: now, CPUs: 8, TotalRAM: 128 << 30, AvailableRAM: 128 << 30, VRAMTotal: &vram, VRAMAvailable: &vram}
	budget, err := NewAdaptiveBudget(Limits{4, 100, 100, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gpu, err := budget.Reserve(snapshot, Need{RAM: 1 << 30, VRAM: 1 << 30}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer gpu()
	if _, err := budget.Reserve(snapshot, Need{RAM: 1, VRAM: 1}, now); err == nil {
		t.Fatal("second GPU request bypassed low VRAM tier")
	}
	var cpu []func()
	defer func() {
		for _, release := range cpu {
			release()
		}
	}()
	for i := 0; i < 3; i++ {
		release, err := budget.Reserve(snapshot, Need{RAM: 1 << 30}, now)
		if err != nil {
			t.Fatal("RAM-only execution inherited unused discrete GPU limit", i, err)
		}
		cpu = append(cpu, release)
	}
	if _, err := budget.Reserve(snapshot, Need{RAM: 1}, now); err == nil {
		t.Fatal("combined requests exceeded configured ceiling")
	}
	for _, release := range cpu {
		release()
	}
	if _, err := budget.Reserve(snapshot, Need{RAM: 1, VRAM: 1}, now); err == nil {
		t.Fatal("CPU releases bypassed remaining GPU reservation")
	}
	gpu()
	snapshot.VRAMTotal, snapshot.VRAMAvailable = nil, nil
	release, err := budget.Reserve(snapshot, Need{RAM: 1}, now)
	if err != nil {
		t.Fatal("unknown unused GPU denied RAM-only execution", err)
	}
	release()
	if _, err := budget.Reserve(snapshot, Need{RAM: 1, VRAM: 1}, now); err == nil {
		t.Fatal("unknown required GPU admitted")
	}
}
