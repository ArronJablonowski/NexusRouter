package resources

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestGPUSurveySourceIsolation(t *testing.T) {
	device := GPUDevice{ID: "card0", Vendor: "amd", Source: "amdgpu-sysfs", TotalBytes: 100, AvailableBytes: 50}
	bad := func(context.Context) ([]GPUDevice, error) {
		return []GPUDevice{device}, errors.New("private driver error")
	}
	good := func(context.Context) ([]GPUDevice, error) { return []GPUDevice{device}, nil }
	out, err := surveyGPUs(context.Background(), "linux", bad, good)
	if err != nil || out.Time.IsZero() || len(out.Sources) != 2 || out.Sources[0].Status != "unavailable" || len(out.Sources[0].Devices) != 0 || out.Sources[1].Status != "observed" || len(out.Sources[1].Devices) != 1 {
		t.Fatalf("source isolation: %+v %v", out, err)
	}
	empty := func(context.Context) ([]GPUDevice, error) { return nil, nil }
	out, err = surveyGPUs(context.Background(), "linux", empty, empty)
	if err != nil || out.Sources[0].Status != "observed" || out.Sources[0].Devices == nil || len(out.Sources[0].Devices) != 0 {
		t.Fatalf("empty observation differs from unavailable: %+v %v", out, err)
	}
}

func TestGPUSurveyUnsupportedAndCanceledDoNotProbe(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context) ([]GPUDevice, error) { calls.Add(1); return nil, nil }
	out, err := surveyGPUs(context.Background(), "darwin", probe, probe)
	if err != nil || calls.Load() != 0 || out.Sources[0].Status != "unsupported" || out.Sources[1].Status != "unsupported" {
		t.Fatalf("unsupported platform: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := surveyGPUs(ctx, "linux", probe, probe); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("canceled survey: %v", err)
	}
}

func TestGPUSurveyProbesConcurrentlyAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	probe := func(ctx context.Context) ([]GPUDevice, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := surveyGPUs(ctx, "linux", probe, probe); done <- err }()
	<-started
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
