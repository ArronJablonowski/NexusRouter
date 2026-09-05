// Package resources measures host capacity and arbitrates local reservations.
package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	GPUs                     *GPUInventory `json:"gpu_inventory,omitempty"`
	Time                     time.Time     `json:"time"`
	CPUs                     int           `json:"cpu_threads"`
	TotalRAM, AvailableRAM   uint64
	SwapUsed                 *uint64
	UnifiedMemory            bool
	VRAMTotal, VRAMAvailable *uint64
	ThermalPressure          *bool
	Source                   string
}

var ErrProfile = errors.New("host resource measurement unavailable")

// Profile does not assume a discrete GPU exists. Missing accelerator/thermal
// metrics remain nil. Darwin's reclaimable memory is a conservative estimate
// from free, inactive and speculative pages, not a promise of allocatable RAM.
func Profile(ctx context.Context) (Snapshot, error) {
	s := Snapshot{Time: time.Now().UTC(), CPUs: runtime.NumCPU()}
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		mem, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return s, ErrProfile
		}
		vm, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
		if err != nil {
			return s, ErrProfile
		}
		s.TotalRAM, err = strconv.ParseUint(strings.TrimSpace(string(mem)), 10, 64)
		if err != nil {
			return s, ErrProfile
		}
		s.AvailableRAM, err = darwinAvailable(string(vm))
		if err != nil || s.AvailableRAM > s.TotalRAM {
			return s, ErrProfile
		}
		arm, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "hw.optional.arm64").Output()
		s.UnifiedMemory = err == nil && strings.TrimSpace(string(arm)) == "1"
		swap, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "vm.swapusage").Output()
		if err == nil {
			fields := strings.Fields(string(swap))
			for i, f := range fields {
				if f == "used" && i+2 < len(fields) {
					v, err := unitBytes(fields[i+2])
					if err == nil {
						s.SwapUsed = &v
					}
					break
				}
			}
		}
		s.Source = "darwin-vm-stat-estimate"
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return s, ErrProfile
		}
		values := map[string]uint64{}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 3 && f[2] == "kB" {
				v, err := strconv.ParseUint(f[1], 10, 64)
				if err != nil || v > ^uint64(0)/1024 {
					return s, ErrProfile
				}
				values[strings.TrimSuffix(f[0], ":")] = v * 1024
			}
		}
		var ok bool
		s.TotalRAM = values["MemTotal"]
		s.AvailableRAM, ok = values["MemAvailable"]
		if !ok || s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM {
			return s, ErrProfile
		}
		if total, ok := values["SwapTotal"]; ok {
			if free, present := values["SwapFree"]; present && free <= total {
				used := total - free
				s.SwapUsed = &used
			}
		}
		s.Source = "linux-proc-meminfo-host"
	default:
		return s, ErrProfile
	}
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	return s, nil
}
func darwinAvailable(text string) (uint64, error) {
	lines := strings.Split(text, "\n")
	var page uint64
	if len(lines) == 0 {
		return 0, ErrProfile
	}
	if _, err := fmt.Sscanf(lines[0], "Mach Virtual Memory Statistics: (page size of %d bytes)", &page); err != nil || page == 0 {
		return 0, ErrProfile
	}
	var pages uint64
	found := 0
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if name != "Pages free" && name != "Pages inactive" && name != "Pages speculative" {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil || v > ^uint64(0)-pages {
			return 0, ErrProfile
		}
		pages += v
		found++
	}
	if found != 3 || pages > ^uint64(0)/page {
		return 0, ErrProfile
	}
	return pages * page, nil
}
func unitBytes(s string) (uint64, error) {
	if len(s) < 2 {
		return 0, ErrProfile
	}
	scale := float64(0)
	switch s[len(s)-1] {
	case 'K':
		scale = 1024
	case 'M':
		scale = 1024 * 1024
	case 'G':
		scale = 1024 * 1024 * 1024
	default:
		return 0, ErrProfile
	}
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || !(n >= 0 && n < 1e18/scale) {
		return 0, ErrProfile
	}
	return uint64(n * scale), nil
}
