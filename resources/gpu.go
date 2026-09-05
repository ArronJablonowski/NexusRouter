package resources

import (
	"context"
	"runtime"
	"time"
)

// GPUDevice describes one observed device, not a fungible allocation pool.
// AvailableBytes is an observation, not proof that an inference backend can
// allocate it. IDs are source-local; AMD card indices may change after reboot.
type GPUDevice struct {
	ID             string `json:"id"`
	Vendor         string `json:"vendor"`
	Source         string `json:"source"`
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type GPUObservation struct {
	Source  string      `json:"source"`
	Status  string      `json:"status"`
	Devices []GPUDevice `json:"devices"`
}

// GPUInventory distinguishes a successful empty survey from an unavailable
// source. It deliberately does not populate Snapshot.VRAMTotal/VRAMAvailable:
// routing needs a verified model/device binding before using those capacities.
type GPUInventory struct {
	Time    time.Time        `json:"time"`
	Sources []GPUObservation `json:"sources"`
}

// SurveyGPUs performs optional read-only diagnostics separately from the hot
// admission path. Linux sources are queried concurrently. Unsupported hosts
// have explicit unsupported results; raw command errors are never exposed.
func SurveyGPUs(ctx context.Context) (GPUInventory, error) {
	return surveyGPUs(ctx, runtime.GOOS, probeNVIDIAGPUs, func(ctx context.Context) ([]GPUDevice, error) {
		return probeAMDGPUs(ctx, "/sys/class/drm")
	})
}

func surveyGPUs(ctx context.Context, platform string, nvidia, amd func(context.Context) ([]GPUDevice, error)) (GPUInventory, error) {
	probes := [2]func(context.Context) ([]GPUDevice, error){nvidia, amd}
	out := GPUInventory{Time: time.Now().UTC(), Sources: []GPUObservation{
		{Source: "nvidia-smi", Status: "unsupported", Devices: []GPUDevice{}},
		{Source: "amdgpu-sysfs", Status: "unsupported", Devices: []GPUDevice{}},
	}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if platform != "linux" {
		return out, nil
	}
	type result struct {
		index   int
		devices []GPUDevice
		err     error
	}
	results := make(chan result, 2)
	for i := range out.Sources {
		go func() {
			devices, err := probes[i](ctx)
			results <- result{i, devices, err}
		}()
	}
	for range out.Sources {
		r := <-results
		out.Sources[r.index].Status = "unavailable"
		if r.err == nil {
			out.Sources[r.index].Status = "observed"
			out.Sources[r.index].Devices = append(out.Sources[r.index].Devices, r.devices...)
		}
	}
	return out, ctx.Err()
}
