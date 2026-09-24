package resources

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const CapacityContractVersion = 1

type CapacityAction string

const (
	CapacityAdmit CapacityAction = "admit"
	CapacityWait  CapacityAction = "wait"
)

const (
	CapacityAvailable = "capacity_available"
	CapacityExhausted = "capacity_exhausted"
	CapacityThermal   = "thermal_pressure"
	CapacitySwap      = "swap_pressure"
)

// CapacityRequest asks how many additional identical workloads fit alongside
// the Budget's live reservations. It never reserves or releases capacity.
type CapacityRequest struct {
	Version  int       `json:"version"`
	Snapshot Snapshot  `json:"snapshot"`
	Need     Need      `json:"need"`
	Now      time.Time `json:"now"`
}

// ValidateNeed admits the model/resource requirement independently of a host
// measurement so callers can reject malformed work before routing or profiling.
func ValidateNeed(need Need) error {
	if need.RAM == 0 || (need.Device != "" && (need.VRAM == 0 || !ValidGPUDeviceID(need.Device))) {
		return ErrResourceData
	}
	return nil
}

// CapacityHeadroom is the usable byte ceiling after observed host usage and
// live DarwinRouter reservations. VRAM is relevant either to the requested
// device or to the snapshot's explicitly aggregated VRAM measurement.
type CapacityHeadroom struct {
	RAMBytes  uint64 `json:"ram_bytes"`
	VRAMBytes uint64 `json:"vram_bytes"`
	VRAMKnown bool   `json:"vram_known"`
	Device    string `json:"device,omitempty"`
}

// CapacityResult is an advisory capacity fact, not execution authorization.
// The application maps Wait into queue, offload, or reject using mode/privacy.
type CapacityResult struct {
	Version       int              `json:"version"`
	Action        CapacityAction   `json:"action"`
	Reason        string           `json:"reason"`
	SnapshotTime  time.Time        `json:"snapshot_time"`
	ObservedAt    time.Time        `json:"observed_at"`
	Headroom      CapacityHeadroom `json:"headroom"`
	MaxAdditional int              `json:"max_additional"`
}

func (r CapacityResult) Validate() error {
	if r.Version != CapacityContractVersion || !capacityResultTime(r.SnapshotTime) || !capacityResultTime(r.ObservedAt) || r.ObservedAt.Before(r.SnapshotTime) || r.MaxAdditional < 0 || r.MaxAdditional > 64 || (r.Headroom.Device != "" && (!ValidGPUDeviceID(r.Headroom.Device) || !r.Headroom.VRAMKnown)) || (!r.Headroom.VRAMKnown && r.Headroom.VRAMBytes != 0) {
		return ErrResourceData
	}
	switch r.Action {
	case CapacityAdmit:
		if r.Reason != CapacityAvailable || r.MaxAdditional < 1 || r.Headroom.RAMBytes == 0 || (r.Headroom.Device != "" && r.Headroom.VRAMBytes == 0) {
			return ErrResourceData
		}
	case CapacityWait:
		if r.MaxAdditional != 0 || (r.Reason != CapacityExhausted && r.Reason != CapacityThermal && r.Reason != CapacitySwap) {
			return ErrResourceData
		}
	default:
		return ErrResourceData
	}
	return nil
}

// Plan returns a conservative, non-mutating view of capacity against the
// budget's current reservations. Invalid/unknown observations fail closed;
// valid transient pressure returns a Wait recommendation.
func (b *Budget) Plan(ctx context.Context, request CapacityRequest) (CapacityResult, error) {
	if ctx == nil || b == nil || request.Version != CapacityContractVersion || ValidateNeed(request.Need) != nil || request.Snapshot.UnifiedMemory && request.Need.VRAM != 0 || !capacityTime(request.Now) {
		return CapacityResult{}, ErrResourceData
	}
	if err := ctx.Err(); err != nil {
		return CapacityResult{}, errors.Join(ErrResourceData, err)
	}
	request.Now = request.Now.UTC()
	snapshot, err := cloneCapacitySnapshot(request.Snapshot, request.Now, b.limits.MaxAge)
	if err != nil {
		return CapacityResult{}, ErrResourceData
	}
	b.mu.Lock()
	clone := &Budget{limits: b.limits, used: b.used, active: b.active, adaptive: b.adaptive, deviceVRAM: cloneCapacityMap(b.deviceVRAM), deviceActive: cloneCapacityMap(b.deviceActive), swapBaseline: cloneCapacityUint(b.swapBaseline)}
	b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CapacityResult{}, errors.Join(ErrResourceData, err)
	}
	headroom := capacityHeadroom(snapshot, request.Need, clone, request.Now)
	if request.Need.VRAM > 0 && !headroom.VRAMKnown {
		return CapacityResult{}, ErrResourceData
	}
	base := CapacityResult{Version: CapacityContractVersion, SnapshotTime: snapshot.Time, ObservedAt: request.Now, Headroom: headroom}
	if snapshot.ThermalPressure != nil && *snapshot.ThermalPressure {
		base.Action, base.Reason = CapacityWait, CapacityThermal
		return checkedCapacity(base)
	}
	if snapshot.SwapPressure != nil && *snapshot.SwapPressure || clone.swapGrowthExceeded(snapshot) {
		base.Action, base.Reason = CapacityWait, CapacitySwap
		return checkedCapacity(base)
	}
	for clone.active < clone.limits.MaxConcurrent {
		if err := ctx.Err(); err != nil {
			return CapacityResult{}, errors.Join(ErrResourceData, err)
		}
		_, reserveErr := clone.Reserve(snapshot, request.Need, request.Now)
		if reserveErr == nil {
			base.MaxAdditional++
			continue
		}
		if errors.Is(reserveErr, ErrResourceData) {
			return CapacityResult{}, ErrResourceData
		}
		break
	}
	if base.MaxAdditional == 0 {
		base.Action, base.Reason = CapacityWait, CapacityExhausted
	} else {
		base.Action, base.Reason = CapacityAdmit, CapacityAvailable
	}
	return checkedCapacity(base)
}

func checkedCapacity(result CapacityResult) (CapacityResult, error) {
	if result.Validate() != nil {
		return CapacityResult{}, ErrResourceData
	}
	return result, nil
}

func capacityHeadroom(snapshot Snapshot, need Need, budget *Budget, now time.Time) CapacityHeadroom {
	ram, _ := headroom(snapshot.TotalRAM, snapshot.AvailableRAM, budget.used.RAM, budget.limits.RAMPercent)
	result := CapacityHeadroom{RAMBytes: ram, Device: need.Device}
	if need.Device != "" {
		total, available, err := DeviceMemory(snapshot, need.Device, now, budget.limits.MaxAge)
		if err == nil {
			result.VRAMBytes, _ = headroom(total, available, budget.deviceVRAM[strings.ToLower(need.Device)], budget.limits.VRAMPercent)
			result.VRAMKnown = true
		}
	} else if snapshot.VRAMTotal != nil && snapshot.VRAMAvailable != nil {
		result.VRAMBytes, _ = headroom(*snapshot.VRAMTotal, *snapshot.VRAMAvailable, budget.used.VRAM, budget.limits.VRAMPercent)
		result.VRAMKnown = true
	}
	return result
}

func cloneCapacitySnapshot(input Snapshot, now time.Time, maxAge time.Duration) (Snapshot, error) {
	if !capacityTime(input.Time) || input.Time.After(now) || now.Sub(input.Time) > maxAge || input.CPUs < 0 || input.CPUs > 4096 || input.TotalRAM == 0 || input.AvailableRAM > input.TotalRAM || len(input.Source) > 128 || !utf8.ValidString(input.Source) || strings.ContainsFunc(input.Source, unicode.IsControl) || (input.VRAMTotal == nil) != (input.VRAMAvailable == nil) || input.UnifiedMemory && input.VRAMTotal != nil {
		return Snapshot{}, ErrResourceData
	}
	if input.VRAMTotal != nil && (*input.VRAMTotal == 0 || *input.VRAMAvailable > *input.VRAMTotal) {
		return Snapshot{}, ErrResourceData
	}
	if input.ThermalState != "" {
		state, pressure := parseDarwinThermal([]byte(input.ThermalState))
		if state != input.ThermalState || pressure == nil || input.ThermalPressure == nil || *pressure != *input.ThermalPressure {
			return Snapshot{}, ErrResourceData
		}
	}
	out := input
	out.Time = input.Time.UTC()
	out.SwapUsed = cloneCapacityUint(input.SwapUsed)
	out.SwapPressure = cloneCapacityBool(input.SwapPressure)
	out.VRAMTotal = cloneCapacityUint(input.VRAMTotal)
	out.VRAMAvailable = cloneCapacityUint(input.VRAMAvailable)
	out.ThermalPressure = cloneCapacityBool(input.ThermalPressure)
	if input.GPUs != nil {
		if !capacityTime(input.GPUs.Time) || input.GPUs.Time.After(now) || now.Sub(input.GPUs.Time) > maxAge || len(input.GPUs.Sources) > 2 {
			return Snapshot{}, ErrResourceData
		}
		inventory := &GPUInventory{Time: input.GPUs.Time.UTC(), Sources: make([]GPUObservation, len(input.GPUs.Sources))}
		seenSources, seenDevices := map[string]bool{}, map[string]bool{}
		for i, source := range input.GPUs.Sources {
			vendor := map[string]string{"nvidia-smi": "nvidia", "amdgpu-sysfs": "amd"}[source.Source]
			if vendor == "" || seenSources[source.Source] || len(source.Devices) > 32 || (source.Status != "observed" && source.Status != "unavailable" && source.Status != "unsupported") || source.Status != "observed" && len(source.Devices) != 0 {
				return Snapshot{}, ErrResourceData
			}
			seenSources[source.Source] = true
			inventory.Sources[i] = GPUObservation{Source: source.Source, Status: source.Status, Devices: make([]GPUDevice, len(source.Devices))}
			for j, device := range source.Devices {
				id := vendor + ":" + device.ID
				canonical := strings.ToLower(id)
				if device.Vendor != vendor || device.Source != source.Source || !ValidGPUDeviceID(id) || device.TotalBytes == 0 || device.AvailableBytes > device.TotalBytes || seenDevices[canonical] {
					return Snapshot{}, ErrResourceData
				}
				seenDevices[canonical] = true
				inventory.Sources[i].Devices[j] = device
			}
		}
		out.GPUs = inventory
	}
	return out, nil
}

func cloneCapacityUint(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneCapacityBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneCapacityMap[K comparable, V any](input map[K]V) map[K]V {
	out := make(map[K]V, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func capacityTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1970 && value.Year() < 2261
}

func capacityResultTime(value time.Time) bool {
	return capacityTime(value) && value.Location() == time.UTC
}
