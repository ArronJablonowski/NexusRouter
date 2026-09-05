package resources

import (
	"errors"
	"testing"
	"time"
)

func TestResourceDataDistinctFromPressure(t *testing.T) {
	for _, mode := range []string{"zero_need", "zero_time", "future", "stale", "zero_total", "invalid_available", "unified_gpu", "missing_gpu", "zero_gpu", "invalid_gpu", "thermal", "ram_pressure", "gpu_pressure", "concurrency"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			s := Snapshot{Time: now, TotalRAM: 1000, AvailableRAM: 1000}
			n := Need{RAM: 1}
			b, _ := NewBudget(Limits{1, 100, 100, time.Second})
			expected := ErrResourceData
			total, available := uint64(1000), uint64(1000)
			switch mode {
			case "zero_need":
				n.RAM = 0
			case "zero_time":
				s.Time = time.Time{}
			case "future":
				s.Time = now.Add(time.Second)
			case "stale":
				s.Time = now.Add(-2 * time.Second)
			case "zero_total":
				s.TotalRAM = 0
			case "invalid_available":
				s.AvailableRAM = 1001
			case "unified_gpu":
				s.UnifiedMemory = true
				n.VRAM = 1
			case "missing_gpu":
				n.VRAM = 1
			case "zero_gpu":
				n.VRAM = 1
				total = 0
				s.VRAMTotal = &total
				s.VRAMAvailable = &available
			case "invalid_gpu":
				n.VRAM = 1
				available = 1001
				s.VRAMTotal = &total
				s.VRAMAvailable = &available
			case "thermal":
				pressure := true
				s.ThermalPressure = &pressure
				expected = ErrCapacity
			case "ram_pressure":
				s.AvailableRAM = 0
				expected = ErrCapacity
			case "gpu_pressure":
				n.VRAM = 1
				available = 0
				s.VRAMTotal = &total
				s.VRAMAvailable = &available
				expected = ErrCapacity
			case "concurrency":
				b.active = 1
				expected = ErrCapacity
			}
			release, err := b.Reserve(s, n, now)
			if release != nil || !errors.Is(err, expected) {
				t.Fatalf("got %v expected %v", err, expected)
			}
			if expected == ErrResourceData && errors.Is(err, ErrCapacity) {
				t.Fatal("data error became pressure")
			}
			if expected == ErrResourceData {
				b.active = 1
				pressure := true
				s.ThermalPressure = &pressure
				if _, err := b.Reserve(s, n, now); !errors.Is(err, ErrResourceData) || errors.Is(err, ErrCapacity) {
					t.Fatalf("pressure masked invalid data: %v", err)
				}
			}
		})
	}
}
