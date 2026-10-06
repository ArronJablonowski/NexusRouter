package config

import "runtime"

// EffectiveRAMPercent applies the Mac-specific allowance without changing Linux limits.
func (h Hardware) EffectiveRAMPercent() float64 {
	if runtime.GOOS == "darwin" {
		if h.MacMemoryPercent > 0 {
			return h.MacMemoryPercent
		}
		return 100
	}
	return h.MaxRAM
}

func (h Hardware) MacSwapGrowthBytes() uint64 {
	if h.MacSwapGrowthGB > 0 {
		return uint64(h.MacSwapGrowthGB * 1e9)
	}
	return 4_000_000_000
}
