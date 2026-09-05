package resources

import (
	"testing"
	"time"
)

// This measures only in-memory admission/release, not hardware probing,
// discovery, provider inference or complete routing latency.
func BenchmarkLocalReservation(b *testing.B) {
	for _, adaptive := range []bool{false, true} {
		name := "fixed"
		if adaptive {
			name = "adaptive"
		}
		b.Run(name, func(b *testing.B) {
			limits := Limits{MaxConcurrent: 8, RAMPercent: 80, VRAMPercent: 85, MaxAge: time.Second}
			constructor := NewBudget
			if adaptive {
				constructor = NewAdaptiveBudget
			}
			budget, err := constructor(limits)
			if err != nil {
				b.Fatal(err)
			}
			now := time.Now()
			snapshot := Snapshot{Time: now, CPUs: 16, TotalRAM: 128 << 30, AvailableRAM: 120 << 30, UnifiedMemory: true}
			need := Need{RAM: 4 << 30}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				release, err := budget.Reserve(snapshot, need, now)
				if err != nil {
					b.Fatal(err)
				}
				release()
			}
		})
	}
}
