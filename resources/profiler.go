// Package resources measures host capacity and arbitrates local reservations.
package resources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	if ctx == nil {
		return s, ErrProfile
	}
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		mem, err := runProbe(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
		if err != nil {
			return s, ErrProfile
		}
		vm, err := runProbe(ctx, "/usr/bin/vm_stat")
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
		arm, err := runProbe(ctx, "/usr/sbin/sysctl", "-n", "hw.optional.arm64")
		s.UnifiedMemory = err == nil && strings.TrimSpace(string(arm)) == "1"
		swap, err := runProbe(ctx, "/usr/sbin/sysctl", "-n", "vm.swapusage")
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
		b, err := readHostMemory(ctx, "/proc/meminfo")
		if err != nil {
			return s, ErrProfile
		}
		s.TotalRAM, s.AvailableRAM, s.SwapUsed, err = parseLinuxMeminfo(b)
		if err != nil {
			return s, ErrProfile
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

// Kernel file reads remain cooperative; byte bounds do not forcibly interrupt a
// stalled kernel read. The production caller uses only the fixed proc path.
func readHostMemory(ctx context.Context, path string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrProfile
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrProfile
	}
	body, err := io.ReadAll(io.LimitReader(f, maxProbeBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(body) > maxProbeBytes {
		return nil, ErrProfile
	}
	return body, nil
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
	found := map[string]bool{}
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if name != "Pages free" && name != "Pages inactive" && name != "Pages speculative" {
			continue
		}
		if found[name] {
			return 0, ErrProfile
		}
		v, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil || v > ^uint64(0)-pages {
			return 0, ErrProfile
		}
		pages += v
		found[name] = true
	}
	if len(found) != 3 || pages > ^uint64(0)/page {
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
