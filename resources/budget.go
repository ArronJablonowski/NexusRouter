package resources

import (
	"errors"
	"math"
	"sync"
	"time"
)

var ErrCapacity = errors.New("local resource capacity unavailable")

type Limits struct {
	MaxConcurrent           int
	RAMPercent, VRAMPercent float64
	MaxAge                  time.Duration
}
type Need struct{ RAM, VRAM uint64 }
type Budget struct {
	mu     sync.Mutex
	limits Limits
	used   Need
	active int
}

func NewBudget(l Limits) (*Budget, error) {
	if l.MaxConcurrent < 1 || l.MaxConcurrent > 64 || l.MaxAge <= 0 || !percent(l.RAMPercent) || !percent(l.VRAMPercent) {
		return nil, ErrCapacity
	}
	return &Budget{limits: l}, nil
}
func percent(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 100 }

// Reserve returns an idempotent release function. Snapshot usage plus all live
// reservations is deliberately conservative: no credit is taken for overlap.
// On unified-memory hosts RAM must include model weights and KV/context memory;
// VRAM is not a second independent pool and must be zero in the request.
func (b *Budget) Reserve(s Snapshot, n Need, now time.Time) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n.RAM == 0 || s.Time.IsZero() || s.Time.After(now) || now.Sub(s.Time) > b.limits.MaxAge || s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM || b.active >= b.limits.MaxConcurrent || (s.ThermalPressure != nil && *s.ThermalPressure) {
		return nil, ErrCapacity
	}
	if s.UnifiedMemory && n.VRAM != 0 {
		return nil, ErrCapacity
	}
	room := func(total, available, reserved, needed uint64, pct float64) bool {
		if available > total {
			return false
		}
		ceiling := uint64(float64(total) * (pct / 100))
		used := total - available
		if used > ceiling || reserved > ceiling-used {
			return false
		}
		return needed <= ceiling-used-reserved
	}
	if !room(s.TotalRAM, s.AvailableRAM, b.used.RAM, n.RAM, b.limits.RAMPercent) {
		return nil, ErrCapacity
	}
	if n.VRAM > 0 {
		if s.VRAMTotal == nil || s.VRAMAvailable == nil || !room(*s.VRAMTotal, *s.VRAMAvailable, b.used.VRAM, n.VRAM, b.limits.VRAMPercent) {
			return nil, ErrCapacity
		}
	}
	b.used.RAM += n.RAM
	b.used.VRAM += n.VRAM
	b.active++
	var once sync.Once
	return func() {
		once.Do(func() { b.mu.Lock(); defer b.mu.Unlock(); b.used.RAM -= n.RAM; b.used.VRAM -= n.VRAM; b.active-- })
	}, nil
}
