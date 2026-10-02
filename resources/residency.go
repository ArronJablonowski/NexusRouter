package resources

import (
	"strings"
	"time"
)

// LowMemory observes the same usable headroom used for adaptive admission,
// including live reservations. It neither reserves capacity nor authorizes an
// unload. Call Reserve with a fresh snapshot after provider lifecycle changes.
// Non-memory pressure and exhausted execution slots return ErrCapacity because
// unloading idle models cannot make those admissions safe.
func (b *Budget) LowMemory(s Snapshot, n Need, now time.Time) (bool, error) {
	if b == nil {
		return false, ErrResourceData
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n.RAM == 0 || s.Time.IsZero() || s.Time.After(now) || now.Sub(s.Time) > b.limits.MaxAge || s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM || (s.UnifiedMemory && n.VRAM != 0) || (n.Device != "" && n.VRAM == 0) {
		return false, ErrResourceData
	}
	if b.active >= b.limits.MaxConcurrent || s.ThermalPressure != nil && *s.ThermalPressure || s.SwapPressure != nil && *s.SwapPressure || b.swapGrowthExceeded(s) {
		return false, ErrCapacity
	}
	// Exhausted headroom is low, even when unloading may later recover some.
	// A failed headroom calculation cannot be interpreted as free capacity.
	room, _ := ramHeadroom(s, b.used.RAM, b.limits.RAMPercent)
	usable := room
	if n.VRAM > 0 {
		var total, available, reserved uint64
		if n.Device != "" {
			var err error
			total, available, err = DeviceMemory(s, n.Device, now, b.limits.MaxAge)
			if err != nil {
				return false, ErrResourceData
			}
			reserved = b.deviceVRAM[strings.ToLower(n.Device)]
		} else {
			if s.VRAMTotal == nil || s.VRAMAvailable == nil || *s.VRAMTotal == 0 || *s.VRAMAvailable > *s.VRAMTotal {
				return false, ErrResourceData
			}
			total, available, reserved = *s.VRAMTotal, *s.VRAMAvailable, b.used.VRAM
		}
		if (n.Device != "" && b.used.VRAM > 0) || (n.Device == "" && len(b.deviceVRAM) > 0) {
			return false, ErrCapacity
		}
		gpuRoom, _ := headroom(total, available, reserved, b.limits.VRAMPercent)
		room = min(room, gpuRoom)
		if n.Device == "" {
			usable = min(usable, gpuRoom)
		} else if b.adaptive && b.deviceActive[strings.ToLower(n.Device)] >= adaptiveTier(gpuRoom, b.limits.MaxConcurrent) {
			return false, ErrCapacity
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
			return false, ErrCapacity
		}
	}
	return room < 16<<30, nil
}
