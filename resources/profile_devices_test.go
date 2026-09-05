package resources

import (
	"context"
	"testing"
	"time"
)

func TestProfileWithGPUsPreservesIndependentObservations(t *testing.T) {
	now := time.Now()
	host := func(context.Context) (Snapshot, error) {
		return Snapshot{Time: now, TotalRAM: 100, AvailableRAM: 50}, nil
	}
	survey := func(context.Context) (GPUInventory, error) {
		return GPUInventory{Time: now, Sources: []GPUObservation{{Source: "nvidia-smi", Status: "unavailable", Devices: []GPUDevice{}}}}, nil
	}
	s, err := profileWithGPUs(context.Background(), host, survey)
	if err != nil || s.TotalRAM != 100 || s.GPUs == nil || s.GPUs.Sources[0].Status != "unavailable" || s.VRAMTotal != nil {
		t.Fatalf("%+v %v", s, err)
	}
	calls := 0
	badHost := func(context.Context) (Snapshot, error) { return Snapshot{}, ErrProfile }
	count := func(context.Context) (GPUInventory, error) { calls++; return GPUInventory{}, nil }
	if _, err := profileWithGPUs(context.Background(), badHost, count); err != ErrProfile || calls != 0 {
		t.Fatal(err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := profileWithGPUs(ctx, host, count); err != ErrProfile || calls != 0 {
		t.Fatal(err, calls)
	}
}
