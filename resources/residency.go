package resources

import (
	"strings"
	"time"
)

// LowMemory observes the same usable headroom used for adaptive admission,
// including live reservations. It neither reserves capacity nor authorizes an
// unload. Call Reserve with a fresh snapshot after provider lifecycle changes.
func (b *Budget) LowMemory(s Snapshot, n Need, now time.Time) (bool, error) {
	if b == nil {
		return false, ErrResourceData
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n.RAM == 0 || s.Time.IsZero() || s.Time.After(now) || now.Sub(s.Time) > b.limits.MaxAge || s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM || (s.UnifiedMemory && n.VRAM != 0) || (n.Device != "" && n.VRAM == 0) {
		return false, ErrResourceData
	}
	if s.ThermalPressure != nil && *s.ThermalPressure {
		return false, ErrCapacity
	}
	// Exhausted headroom is low, even when unloading may later recover some.
	// A failed headroom calculation cannot be interpreted as free capacity.
	room, _ := headroom(s.TotalRAM, s.AvailableRAM, b.used.RAM, b.limits.RAMPercent)
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
	}
	return room < 16<<30, nil
}
