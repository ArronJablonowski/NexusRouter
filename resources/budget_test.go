package resources

import (
	"sync"
	"testing"
	"time"
)

func TestReservationsAndPressure(t *testing.T) {
	now := time.Now()
	s := Snapshot{Time: now, TotalRAM: 1000, AvailableRAM: 800, UnifiedMemory: true}
	b, err := NewBudget(Limits{MaxConcurrent: 2, RAMPercent: 80, VRAMPercent: 85, MaxAge: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	a, err := b.Reserve(s, Need{RAM: 400}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(s, Need{RAM: 201}, now); err == nil {
		t.Fatal("oversubscribed RAM")
	}
	c, err := b.Reserve(s, Need{RAM: 200}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(s, Need{RAM: 1}, now); err == nil {
		t.Fatal("concurrency exceeded")
	}
	a()
	a()
	c()
	for _, name := range []string{"stale", "thermal", "swap", "gpu_unknown", "unified_double_count", "future"} {
		t.Run(name, func(t *testing.T) {
			copy := s
			n := Need{RAM: 1}
			switch name {
			case "stale":
				copy.Time = now.Add(-2 * time.Second)
			case "future":
				copy.Time = now.Add(time.Second)
			case "thermal":
				v := true
				copy.ThermalPressure = &v
			case "swap":
				v := true
				copy.SwapPressure = &v
			case "gpu_unknown":
				copy.UnifiedMemory = false
				n.VRAM = 1
			case "unified_double_count":
				n.VRAM = 1
			}
			if _, err := b.Reserve(copy, n, now); err == nil {
				t.Fatal("unsafe reservation accepted")
			}
		})
	}
}
func TestConcurrentReservations(t *testing.T) {
	now := time.Now()
	b, _ := NewBudget(Limits{3, 80, 85, time.Second})
	s := Snapshot{Time: now, TotalRAM: 1000, AvailableRAM: 1000}
	var wg sync.WaitGroup
	releases := make(chan func(), 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, err := b.Reserve(s, Need{RAM: 100}, now); err == nil {
				releases <- release
			}
		}()
	}
	wg.Wait()
	close(releases)
	n := 0
	for release := range releases {
		n++
		release()
	}
	if n != 3 {
		t.Fatal("reservation count", n)
	}
}

func TestSwapGrowthGuardRejectsMoreThanFiveGiB(t *testing.T) {
	now := time.Now()
	baseline := uint64(3 << 30)
	b, err := NewBudget(Limits{MaxConcurrent: 1, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Time: now, TotalRAM: 16 << 30, AvailableRAM: 16 << 30, SwapUsed: &baseline}
	release, err := b.Reserve(snapshot, Need{RAM: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	release()
	atBoundary := baseline + 5<<30
	snapshot.SwapUsed = &atBoundary
	release, err = b.Reserve(snapshot, Need{RAM: 1}, now)
	if err != nil {
		t.Fatal("5 GiB boundary rejected", err)
	}
	release()
	over := atBoundary + 1
	snapshot.SwapUsed = &over
	if _, err = b.Reserve(snapshot, Need{RAM: 1}, now); err == nil {
		t.Fatal("swap growth over 5 GiB admitted")
	}
}
func TestDarwinParsing(t *testing.T) {
	input := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 2.\nPages inactive: 3.\nPages speculative: 1.\n"
	n, err := darwinAvailable(input)
	if err != nil || n != 6*16384 {
		t.Fatal(n, err)
	}
	if _, err := darwinAvailable("malformed"); err == nil {
		t.Fatal("malformed statistics accepted")
	}
	n, err = unitBytes("1.5M")
	if err != nil || n != 1572864 {
		t.Fatal(n, err)
	}
}
