package resources

import (
	"strconv"
	"strings"
	"time"
)

// ValidGPUDeviceID accepts source-qualified identifiers, never device paths.
func ValidGPUDeviceID(id string) bool {
	if strings.HasPrefix(id, "nvidia:") {
		return nvidiaUUID(strings.TrimPrefix(id, "nvidia:"))
	}
	if !strings.HasPrefix(id, "amd:card") {
		return false
	}
	digits := strings.TrimPrefix(id, "amd:card")
	n, err := strconv.ParseUint(digits, 10, 32)
	return err == nil && strconv.FormatUint(n, 10) == digits
}

// DeviceMemory refuses ambiguous inventories; independent devices are not an
// aggregate allocation pool. Unavailable unrelated sources may remain present.
func DeviceMemory(s Snapshot, device string, now time.Time, maxAge time.Duration) (uint64, uint64, error) {
	if !ValidGPUDeviceID(device) || s.UnifiedMemory || s.GPUs == nil || maxAge <= 0 || s.GPUs.Time.IsZero() || s.GPUs.Time.After(now) || now.Sub(s.GPUs.Time) > maxAge || len(s.GPUs.Sources) > 2 {
		return 0, 0, ErrResourceData
	}
	seenSources := map[string]bool{}
	seenDevices := map[string]bool{}
	var total, available uint64
	found := false
	for _, source := range s.GPUs.Sources {
		vendor := ""
		switch source.Source {
		case "nvidia-smi":
			vendor = "nvidia"
		case "amdgpu-sysfs":
			vendor = "amd"
		default:
			return 0, 0, ErrResourceData
		}
		if seenSources[source.Source] || len(source.Devices) > 32 {
			return 0, 0, ErrResourceData
		}
		seenSources[source.Source] = true
		if source.Status != "observed" {
			if (source.Status != "unavailable" && source.Status != "unsupported") || len(source.Devices) > 0 {
				return 0, 0, ErrResourceData
			}
			continue
		}
		for _, gpu := range source.Devices {
			id := vendor + ":" + gpu.ID
			canonical := strings.ToLower(id)
			if gpu.Vendor != vendor || gpu.Source != source.Source || !ValidGPUDeviceID(id) || gpu.TotalBytes == 0 || gpu.AvailableBytes > gpu.TotalBytes || seenDevices[canonical] {
				return 0, 0, ErrResourceData
			}
			seenDevices[canonical] = true
			if strings.EqualFold(id, device) {
				total, available, found = gpu.TotalBytes, gpu.AvailableBytes, true
			}
		}
	}
	if !found {
		return 0, 0, ErrResourceData
	}
	return total, available, nil
}
