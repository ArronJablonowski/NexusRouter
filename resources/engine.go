package resources

import (
	"context"
	"errors"
	"reflect"
)

// Profiler is a trusted in-process measurement extension, not a sandbox. Its
// callback must honor context cancellation, support concurrent calls, and
// return owned immutable data. The adapter cannot interrupt a blocked callback.
type Profiler interface {
	Measure(context.Context) (Measurement, error)
}

type Measurement struct {
	Version  int
	Snapshot Snapshot
}

// HostProfiler selects the built-in OS measurements, optionally surveying GPUs.
type HostProfiler struct{ IncludeGPUs bool }

func (p HostProfiler) Measure(ctx context.Context) (Measurement, error) {
	var snapshot Snapshot
	var err error
	if p.IncludeGPUs {
		snapshot, err = ProfileWithGPUs(ctx)
	} else {
		snapshot, err = Profile(ctx)
	}
	if err != nil {
		return Measurement{}, err
	}
	return Measurement{Version: 1, Snapshot: snapshot}, nil
}

// MeasureSnapshot detaches mutable measurement fields and labels provenance
// without copying arbitrary profiler text into durable routing evidence.
// Numeric validity and freshness remain admission decisions in Budget.
func MeasureSnapshot(ctx context.Context, profiler Profiler) (snapshot Snapshot, err error) {
	defer func() {
		if recover() != nil {
			snapshot = Snapshot{}
			err = ErrProfile
			if ctx != nil {
				err = errors.Join(err, ctx.Err())
			}
		}
	}()
	if ctx == nil || profiler == nil {
		return Snapshot{}, ErrProfile
	}
	value := reflect.ValueOf(profiler)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		if value.IsNil() {
			return Snapshot{}, ErrProfile
		}
	}
	if ctx.Err() != nil {
		return Snapshot{}, errors.Join(ErrProfile, ctx.Err())
	}
	measurement, measureErr := profiler.Measure(ctx)
	if ctx.Err() != nil {
		return Snapshot{}, errors.Join(ErrProfile, ctx.Err())
	}
	if measureErr != nil {
		if errors.Is(measureErr, context.Canceled) {
			return Snapshot{}, errors.Join(ErrProfile, context.Canceled)
		}
		if errors.Is(measureErr, context.DeadlineExceeded) {
			return Snapshot{}, errors.Join(ErrProfile, context.DeadlineExceeded)
		}
		return Snapshot{}, ErrProfile
	}
	if measurement.Version != 1 || len(measurement.Snapshot.Source) > 128 {
		return Snapshot{}, ErrProfile
	}
	snapshot = measurement.Snapshot
	cloneUint := func(p *uint64) *uint64 {
		if p == nil {
			return nil
		}
		value := *p
		return &value
	}
	snapshot.SwapUsed = cloneUint(snapshot.SwapUsed)
	snapshot.VRAMTotal = cloneUint(snapshot.VRAMTotal)
	snapshot.VRAMAvailable = cloneUint(snapshot.VRAMAvailable)
	if snapshot.ThermalPressure != nil {
		value := *snapshot.ThermalPressure
		snapshot.ThermalPressure = &value
	}
	if measurement.Snapshot.GPUs != nil {
		original := measurement.Snapshot.GPUs
		if len(original.Sources) > 2 {
			return Snapshot{}, ErrProfile
		}
		inventory := &GPUInventory{Time: original.Time, Sources: make([]GPUObservation, len(original.Sources))}
		seenSources := map[string]bool{}
		for i, source := range original.Sources {
			if seenSources[source.Source] {
				return Snapshot{}, ErrProfile
			}
			seenSources[source.Source] = true
			if (source.Source != "nvidia-smi" && source.Source != "amdgpu-sysfs") || (source.Status != "observed" && source.Status != "unavailable" && source.Status != "unsupported") || len(source.Devices) > 32 {
				return Snapshot{}, ErrProfile
			}
			if source.Status != "observed" && len(source.Devices) != 0 {
				return Snapshot{}, ErrProfile
			}
			inventory.Sources[i] = GPUObservation{Source: source.Source, Status: source.Status, Devices: make([]GPUDevice, len(source.Devices))}
			for j, device := range source.Devices {
				vendor := "nvidia"
				if source.Source == "amdgpu-sysfs" {
					vendor = "amd"
				}
				if len(device.ID) > 128 || device.Source != source.Source || device.Vendor != vendor || !ValidGPUDeviceID(vendor+":"+device.ID) {
					return Snapshot{}, ErrProfile
				}
				inventory.Sources[i].Devices[j] = device
			}
		}
		snapshot.GPUs = inventory
	}
	snapshot.Source = "embedded-resource-profiler"
	return snapshot, nil
}
