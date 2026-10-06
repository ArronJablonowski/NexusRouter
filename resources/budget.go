package resources

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"
)

var ErrCapacity = errors.New("local resource capacity unavailable")

// ErrResourceData identifies unknown or invalid resource facts, not transient
// pressure. Waiting cannot safely turn these facts into an admission decision.
var ErrResourceData = errors.New("local resource data unavailable")

type Limits struct {
	MaxConcurrent           int
	RAMPercent, VRAMPercent float64
	MaxAge                  time.Duration
}
type Need struct {
	BackendManagedRAM bool
	RAM, VRAM         uint64
	Device            string
}
type Budget struct {
	mu           sync.Mutex
	limits       Limits
	used         Need
	active       int
	adaptive     bool
	deviceVRAM   map[string]uint64
	deviceActive map[string]int
	swapBaseline *uint64
}

// NewAdaptiveBudget uses MaxConcurrent as an upper ceiling, deriving each new
// admission's concurrency limit from remaining measured resource headroom.
func NewAdaptiveBudget(l Limits) (*Budget, error) {
	b, err := NewBudget(l)
	if err != nil {
		return nil, err
	}
	b.adaptive = true
	return b, nil
}

// byteCeiling floors the exact rational value of the supplied float percentage.
// Converting total to float first can round upward or overflow at MaxUint64.
func byteCeiling(total uint64, pct float64) uint64 {
	ratio := new(big.Rat).SetFloat64(pct)
	ratio.Mul(ratio, new(big.Rat).SetInt(new(big.Int).SetUint64(total)))
	ratio.Quo(ratio, big.NewRat(100, 1))
	return new(big.Int).Quo(ratio.Num(), ratio.Denom()).Uint64()
}

func headroom(total, available, reserved uint64, pct float64) (uint64, bool) {
	if available > total {
		return 0, false
	}
	ceiling := byteCeiling(total, pct)
	used := total - available
	if used > ceiling || reserved > ceiling-used {
		return 0, false
	}
	return ceiling - used - reserved, true
}

// SparkRAMReserveBytes preserves at least 8 GiB of measured unified-memory
// headroom. This is larger than 8 decimal GB. It is not a kernel allocation cap.
const SparkRAMReserveBytes uint64 = 8 << 30

// ramHeadroom applies the stricter of the percentage ceiling and platform free
// reserve, then charges every live reservation exactly once against that room.
func ramHeadroom(s Snapshot, reserved uint64, pct float64) (uint64, bool) {
	room, ok := headroom(s.TotalRAM, s.AvailableRAM, reserved, pct)
	if !ok || s.AvailableRAM < s.RAMReserveBytes || reserved > s.AvailableRAM-s.RAMReserveBytes {
		return 0, false
	}
	return min(room, s.AvailableRAM-s.RAMReserveBytes-reserved), true
}

func NewBudget(l Limits) (*Budget, error) {
	if l.MaxConcurrent < 1 || l.MaxConcurrent > 64 || l.MaxAge <= 0 || !percent(l.RAMPercent) || !percent(l.VRAMPercent) {
		return nil, ErrCapacity
	}
	return &Budget{limits: l, deviceVRAM: map[string]uint64{}, deviceActive: map[string]int{}}, nil
}

func (b *Budget) swapGrowthExceeded(s Snapshot) bool {
	if s.SwapUsed == nil {
		return false
	}
	if b.swapBaseline == nil {
		baseline := *s.SwapUsed
		b.swapBaseline = &baseline
		return false
	}
	return *s.SwapUsed > *b.swapBaseline && *s.SwapUsed-*b.swapBaseline > 5<<30
}
func percent(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 100 }

// Reserve returns an idempotent release function. Snapshot usage plus all live
// reservations is deliberately conservative: no credit is taken for overlap.
// On unified-memory hosts RAM must include model weights and KV/context memory;
// VRAM is not a second independent pool and must be zero in the request.
func (b *Budget) Reserve(s Snapshot, n Need, now time.Time) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	deviceKey := strings.ToLower(n.Device)
	if n.RAM == 0 || s.Time.IsZero() || s.Time.After(now) || now.Sub(s.Time) > b.limits.MaxAge || s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM {
		return nil, ErrResourceData
	}
	if s.UnifiedMemory && n.VRAM != 0 {
		return nil, ErrResourceData
	}
	if n.Device != "" && n.VRAM == 0 {
		return nil, ErrResourceData
	}
	var gpuTotal, gpuAvailable, gpuReserved uint64
	if n.Device != "" {
		var err error
		gpuTotal, gpuAvailable, err = DeviceMemory(s, n.Device, now, b.limits.MaxAge)
		if err != nil {
			return nil, ErrResourceData
		}
		gpuReserved = b.deviceVRAM[deviceKey]
	} else if n.VRAM > 0 && (s.VRAMTotal == nil || s.VRAMAvailable == nil || *s.VRAMTotal == 0 || *s.VRAMAvailable > *s.VRAMTotal) {
		return nil, ErrResourceData
	} else if n.VRAM > 0 {
		gpuTotal, gpuAvailable, gpuReserved = *s.VRAMTotal, *s.VRAMAvailable, b.used.VRAM
	}
	if n.VRAM > 0 && ((n.Device != "" && b.used.VRAM > 0) || (n.Device == "" && len(b.deviceVRAM) > 0)) {
		return nil, ErrCapacity
	}
	if b.active >= b.limits.MaxConcurrent || (s.ThermalPressure != nil && *s.ThermalPressure) || (s.SwapPressure != nil && *s.SwapPressure) || b.swapGrowthExceeded(s) {
		return nil, ErrCapacity
	}
	ramRoom, ok := ramHeadroom(s, b.used.RAM, b.limits.RAMPercent)
	if n.BackendManagedRAM {
		// Explicit Mac backend policy: Ollama owns residency and reclamation.
		// Retain physical-capacity, concurrent-reservation and pressure limits.
		if s.Source != "darwin-vm-stat-estimate" || !s.UnifiedMemory || s.SwapUsed == nil || s.ThermalPressure == nil || n.VRAM != 0 || n.Device != "" {
			return nil, ErrResourceData
		}
		ceiling := byteCeiling(s.TotalRAM, b.limits.RAMPercent)
		ok = b.used.RAM <= ceiling
		if ok {
			ramRoom = ceiling - b.used.RAM
		}
	}
	if !ok || n.RAM > ramRoom {
		return nil, ErrCapacity
	}
	usable := ramRoom
	if n.VRAM > 0 {
		gpuRoom, ok := headroom(gpuTotal, gpuAvailable, gpuReserved, b.limits.VRAMPercent)
		if !ok || n.VRAM > gpuRoom {
			return nil, ErrCapacity
		}
		if n.Device == "" {
			usable = min(usable, gpuRoom)
		} else if b.adaptive && b.deviceActive[deviceKey] >= adaptiveTier(gpuRoom, b.limits.MaxConcurrent) {
			return nil, ErrCapacity
		}
	}
	if b.adaptive {
		limit := adaptiveTier(usable, b.limits.MaxConcurrent)
		if s.CPUs <= 0 {
			limit = 1
		} else {
			limit = min(limit, s.CPUs)
		}
		if b.active >= limit {
			return nil, ErrCapacity
		}
	}
	b.used.RAM += n.RAM
	if n.Device == "" {
		b.used.VRAM += n.VRAM
	} else {
		b.deviceVRAM[deviceKey] += n.VRAM
		b.deviceActive[deviceKey]++
	}
	b.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.used.RAM -= n.RAM
			if n.Device == "" {
				b.used.VRAM -= n.VRAM
			} else {
				b.deviceVRAM[deviceKey] -= n.VRAM
				b.deviceActive[deviceKey]--
				if b.deviceVRAM[deviceKey] == 0 {
					delete(b.deviceVRAM, deviceKey)
					delete(b.deviceActive, deviceKey)
				}
			}
			b.active--
		})
	}, nil
}

func adaptiveTier(usable uint64, ceiling int) int {
	if usable < 16<<30 {
		return 1
	}
	if usable <= 64<<30 {
		return min(ceiling, 2)
	}
	return ceiling
}
