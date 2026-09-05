package resources

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// probeAMDGPUs reads only kernel-exported byte counters, not model estimates.
// Sysfs card/device links are followed intentionally. Only canonical card names
// from a bounded directory listing can contribute path components.
// Missing PCI vendor files fail closed (non-PCI DRM surveys are unsupported).
// The kernel documents mem_info_vram_total and mem_info_vram_used as bytes:
// https://www.kernel.org/doc/html/latest/gpu/amdgpu/driver-misc.html#mem-info-vram-total
func probeAMDGPUs(ctx context.Context, drmRoot string) ([]GPUDevice, error) {
	if ctx.Err() != nil {
		return nil, ErrProfile
	}
	dir, err := os.Open(drmRoot)
	if err != nil {
		return nil, ErrProfile
	}
	entries, err := dir.ReadDir(257)
	closeErr := dir.Close()
	if err != nil && err != io.EOF || closeErr != nil || len(entries) > 256 {
		return nil, ErrProfile
	}
	cards := []string{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ErrProfile
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "card") {
			continue
		}
		number := strings.TrimPrefix(name, "card")
		n, err := strconv.ParseUint(number, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != number {
			continue
		}
		cards = append(cards, name)
		if len(cards) > 32 {
			return nil, ErrProfile
		}
	}
	sort.Strings(cards)
	devices := []GPUDevice{}
	for _, card := range cards {
		base := filepath.Join(drmRoot, card, "device")
		vendor, err := readAMDSysfs(ctx, filepath.Join(base, "vendor"))
		if err != nil {
			return nil, ErrProfile
		}
		if len(vendor) != 6 || !strings.HasPrefix(vendor, "0x") {
			return nil, ErrProfile
		}
		for _, digit := range vendor[2:] {
			if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f' || digit >= 'A' && digit <= 'F') {
				return nil, ErrProfile
			}
		}
		if vendor != "0x1002" {
			continue
		}
		totalText, err := readAMDSysfs(ctx, filepath.Join(base, "mem_info_vram_total"))
		if err != nil {
			return nil, ErrProfile
		}
		usedText, err := readAMDSysfs(ctx, filepath.Join(base, "mem_info_vram_used"))
		if err != nil {
			return nil, ErrProfile
		}
		total, totalErr := amdDecimalBytes(totalText)
		used, usedErr := amdDecimalBytes(usedText)
		if totalErr != nil || usedErr != nil || total == 0 || used > total {
			return nil, ErrProfile
		}
		devices = append(devices, GPUDevice{ID: card, Vendor: "amd", Source: "amdgpu-sysfs", TotalBytes: total, AvailableBytes: total - used})
	}
	if ctx.Err() != nil {
		return nil, ErrProfile
	}
	return devices, nil
}

func readAMDSysfs(ctx context.Context, path string) (string, error) {
	if ctx.Err() != nil {
		return "", ErrProfile
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrProfile
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrProfile
	}
	body, err := io.ReadAll(io.LimitReader(f, 65))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(body) > 64 || ctx.Err() != nil {
		return "", ErrProfile
	}
	return strings.TrimSuffix(string(body), "\n"), nil
}

func amdDecimalBytes(value string) (uint64, error) {
	if value == "" {
		return 0, ErrProfile
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, ErrProfile
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, ErrProfile
	}
	return n, nil
}
