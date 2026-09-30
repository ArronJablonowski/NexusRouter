package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

func sdkResourcePlanClient(t *testing.T, mode, concurrent, pressure string, profiler resources.Profiler, builds *atomic.Int32) (*sdk.Client, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = mode
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = concurrent
	cfg.Hardware.LocalPressurePolicy = pressure
	cfg.Hardware.MaxRAM, cfg.Hardware.MaxVRAM = 100, 100
	cfg.Workers.Max = 4
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "resource-plan.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	locality := "local"
	if mode == "cloud_only" {
		locality = "cloud"
	}
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: locality, RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{
		ProjectFile:      path,
		ResourceProfiler: profiler,
		ProviderFactory: sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
			builds.Add(1)
			return nil, errors.New("provider construction must not run")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, cfg.Telemetry.Database
}

func sdkPlanMeasurement(mutator func(*resources.Snapshot)) sdkFixtureProfiler {
	return func(context.Context) (resources.Measurement, error) {
		swap, thermal := uint64(0), false
		snapshot := resources.Snapshot{Time: time.Now().UTC(), CPUs: 8, TotalRAM: 128 << 30, AvailableRAM: 128 << 30, UnifiedMemory: true, SwapUsed: &swap, ThermalPressure: &thermal, Source: "resource-plan-fixture"}
		if mutator != nil {
			mutator(&snapshot)
		}
		return resources.Measurement{Version: 1, Snapshot: snapshot}, nil
	}
}

func TestSDKResourcePlanIsReadOnlyDetachedAndBounded(t *testing.T) {
	var builds, measurements atomic.Int32
	profiler := sdkPlanMeasurement(func(*resources.Snapshot) { measurements.Add(1) })
	client, database := sdkResourcePlanClient(t, "local_only", "auto", "reject", profiler, &builds)
	request := sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1 << 30, LocalRequired: true}
	first, err := client.ResourcePlan(context.Background(), request)
	if err != nil || first.Validate() != nil || first.Action != sdk.ResourcePlanExecuteLocal || first.Pressure || first.MaxAdditionalLocal != 4 || first.RAMHeadroomBytes == 0 || first.VRAMKnown || builds.Load() != 0 || measurements.Load() != 1 {
		t.Fatal(first, err, builds.Load(), measurements.Load())
	}
	first.Reason = "caller mutation"
	first.RAMHeadroomBytes = 0
	second, err := client.ResourcePlan(context.Background(), request)
	if err != nil || second.Validate() != nil || second.Reason == first.Reason || second.RAMHeadroomBytes == 0 || measurements.Load() != 2 {
		t.Fatal(second, err)
	}
	if _, err = os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning created durable storage", err)
	}
}

func TestSDKResourcePlanMapsModePrivacyAndPressure(t *testing.T) {
	cases := []struct {
		name, mode, pressurePolicy string
		localRequired              bool
		profiler                   resources.Profiler
		want                       sdk.ResourcePlanAction
		wantPressure               bool
	}{
		{"local-queue", "local_only", "wait", true, sdkPlanMeasurement(func(s *resources.Snapshot) { *s.ThermalPressure = true }), sdk.ResourcePlanQueue, true},
		{"local-swap-queue", "local_only", "wait", true, sdkPlanMeasurement(func(s *resources.Snapshot) { active := true; s.SwapPressure = &active }), sdk.ResourcePlanQueue, true},
		{"local-reject", "local_only", "reject", true, sdkPlanMeasurement(func(s *resources.Snapshot) { s.AvailableRAM = 0 }), sdk.ResourcePlanReject, true},
		{"hybrid-offload", "hybrid", "reject", false, sdkPlanMeasurement(func(s *resources.Snapshot) { s.AvailableRAM = 0 }), sdk.ResourcePlanOffload, true},
		{"hybrid-wait-offload", "hybrid", "wait", false, sdkPlanMeasurement(func(s *resources.Snapshot) { s.AvailableRAM = 0 }), sdk.ResourcePlanOffload, true},
		{"hybrid-private-queue", "hybrid", "wait", true, sdkPlanMeasurement(func(s *resources.Snapshot) { s.AvailableRAM = 0 }), sdk.ResourcePlanQueue, true},
		{"cloud-offload", "cloud_only", "reject", false, sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { panic("profiler must not run") }), sdk.ResourcePlanOffload, false},
		{"cloud-private-reject", "cloud_only", "wait", true, sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { panic("profiler must not run") }), sdk.ResourcePlanReject, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var builds atomic.Int32
			client, database := sdkResourcePlanClient(t, tc.mode, "auto", tc.pressurePolicy, tc.profiler, &builds)
			result, err := client.ResourcePlan(context.Background(), sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1 << 30, LocalRequired: tc.localRequired})
			if err != nil || result.Validate() != nil || result.Action != tc.want || result.Pressure != tc.wantPressure || builds.Load() != 0 {
				t.Fatal(result, err, builds.Load())
			}
			if _, err = os.Stat(database); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("planning created database", err)
			}
		})
	}
}

type sdkTypedNilProfiler struct{}

func (*sdkTypedNilProfiler) Measure(context.Context) (resources.Measurement, error) {
	panic("typed nil profiler invoked")
}

func TestSDKResourcePlanRejectsInvalidProfilerAndRequests(t *testing.T) {
	profilers := map[string]resources.Profiler{
		"error": sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
			return resources.Measurement{}, errors.New("private resource-plan detail")
		}),
		"panic":      sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { panic("private resource-plan detail") }),
		"stale":      sdkPlanMeasurement(func(s *resources.Snapshot) { s.Time = time.Now().Add(-time.Hour).UTC() }),
		"future":     sdkPlanMeasurement(func(s *resources.Snapshot) { s.Time = time.Now().Add(time.Hour).UTC() }),
		"impossible": sdkPlanMeasurement(func(s *resources.Snapshot) { s.AvailableRAM = s.TotalRAM + 1 }),
		"oversized-source": sdkPlanMeasurement(func(s *resources.Snapshot) {
			s.Source = strings.Repeat("x", 129)
		}),
		"oversized-gpu-inventory": sdkPlanMeasurement(func(s *resources.Snapshot) {
			s.UnifiedMemory = false
			s.GPUs = &resources.GPUInventory{Time: s.Time, Sources: []resources.GPUObservation{{Source: "nvidia-smi", Status: "unsupported"}, {Source: "amdgpu-sysfs", Status: "unsupported"}, {Source: "nvidia-smi", Status: "unsupported"}}}
		}),
	}
	var typedNil *sdkTypedNilProfiler
	profilers["typed-nil"] = typedNil
	for name, profiler := range profilers {
		t.Run(name, func(t *testing.T) {
			var builds atomic.Int32
			client, database := sdkResourcePlanClient(t, "local_only", "auto", "reject", profiler, &builds)
			result, err := client.ResourcePlan(context.Background(), sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1})
			if !errors.Is(err, sdk.ErrAdmission) || result.Version != 1 || result.Action != "" || builds.Load() != 0 || strings.Contains(err.Error(), "private") {
				t.Fatal(result, err, builds.Load())
			}
			if _, statErr := os.Stat(database); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("invalid plan created database", statErr)
			}
		})
	}
	var builds atomic.Int32
	client, _ := sdkResourcePlanClient(t, "local_only", "auto", "reject", sdkPlanMeasurement(nil), &builds)
	for _, request := range []sdk.ResourcePlanRequest{
		{},
		{Version: 2, RAMBytes: 1},
		{Version: 1},
		{Version: 1, RAMBytes: 1, GPUDevice: "nvidia:GPU-abcdef01"},
		{Version: 1, RAMBytes: 1, VRAMBytes: 1, GPUDevice: "../../device"},
	} {
		if result, err := client.ResourcePlan(context.Background(), request); !errors.Is(err, sdk.ErrAdmission) || result.Version != 1 {
			t.Fatal(request, result, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ResourcePlan(canceled, sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1}); !errors.Is(err, context.Canceled) || !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
}

func TestSDKResourcePlanDiscreteGPUAndUnifiedMemory(t *testing.T) {
	deviceProfiler := sdkPlanMeasurement(func(snapshot *resources.Snapshot) {
		snapshot.UnifiedMemory = false
		snapshot.GPUs = &resources.GPUInventory{Time: snapshot.Time, Sources: []resources.GPUObservation{{Source: "nvidia-smi", Status: "observed", Devices: []resources.GPUDevice{{ID: "GPU-abcdef01", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: 16 << 30, AvailableBytes: 16 << 30}}}}}
	})
	var builds atomic.Int32
	client, _ := sdkResourcePlanClient(t, "local_only", "auto", "reject", deviceProfiler, &builds)
	request := sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1 << 30, VRAMBytes: 2 << 30, GPUDevice: "nvidia:GPU-abcdef01", LocalRequired: true}
	result, err := client.ResourcePlan(context.Background(), request)
	if err != nil || result.Validate() != nil || result.Action != sdk.ResourcePlanExecuteLocal || result.GPUDevice != request.GPUDevice || !result.VRAMKnown || result.VRAMHeadroomBytes == 0 || result.MaxAdditionalLocal != 1 {
		t.Fatal(result, err)
	}
	for name, profiler := range map[string]resources.Profiler{
		"unknown-device": deviceProfiler,
		"unified-vram":   sdkPlanMeasurement(nil),
		"missing-vram": sdkPlanMeasurement(func(snapshot *resources.Snapshot) {
			snapshot.UnifiedMemory = false
		}),
	} {
		t.Run(name, func(t *testing.T) {
			var builds atomic.Int32
			candidate, database := sdkResourcePlanClient(t, "local_only", "auto", "reject", profiler, &builds)
			bad := sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1, VRAMBytes: 1}
			if name == "unknown-device" {
				bad.GPUDevice = "nvidia:GPU-deadbeef"
			}
			out, err := candidate.ResourcePlan(context.Background(), bad)
			if !errors.Is(err, sdk.ErrAdmission) || out.Action != "" || builds.Load() != 0 {
				t.Fatal(out, err, builds.Load())
			}
			if _, statErr := os.Stat(database); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("invalid GPU plan created database", statErr)
			}
		})
	}
}

func TestSDKResourcePlanCancellationDuringProfiler(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	profiler := sdkFixtureProfiler(func(ctx context.Context) (resources.Measurement, error) {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return resources.Measurement{}, ctx.Err()
	})
	var builds atomic.Int32
	client, database := sdkResourcePlanClient(t, "local_only", "auto", "reject", profiler, &builds)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.ResourcePlan(ctx, sdk.ResourcePlanRequest{Version: 1, RAMBytes: 1})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("profiler did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, sdk.ErrAdmission) || !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resource plan did not return after cancellation")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("profiler callback survived return")
	}
	if builds.Load() != 0 {
		t.Fatal("canceled plan built provider")
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled plan created database", err)
	}
}

func TestSDKResourcePlanResultValidation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	valid := sdk.ResourcePlanResult{Version: 1, Action: sdk.ResourcePlanExecuteLocal, Reason: resources.CapacityAvailable, SnapshotTime: now, ObservedAt: now, RAMHeadroomBytes: 1, MaxAdditionalLocal: 1}
	if valid.Validate() != nil {
		t.Fatal(valid.Validate())
	}
	for _, mutate := range []func(*sdk.ResourcePlanResult){
		func(result *sdk.ResourcePlanResult) { result.Version = 2 },
		func(result *sdk.ResourcePlanResult) { result.RAMHeadroomBytes = 0 },
		func(result *sdk.ResourcePlanResult) {
			result.SnapshotTime = result.SnapshotTime.In(time.FixedZone("same", 0))
		},
		func(result *sdk.ResourcePlanResult) { result.ObservedAt = time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC) },
		func(result *sdk.ResourcePlanResult) { result.GPUDevice, result.VRAMKnown = "nvidia:GPU-abcdef01", true },
	} {
		bad := valid
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid public plan accepted", bad)
		}
	}
	for _, bad := range []sdk.ResourcePlanResult{
		{Version: 1, Action: sdk.ResourcePlanOffload, Reason: "cloud_only", RAMHeadroomBytes: 1},
		{Version: 1, Action: sdk.ResourcePlanOffload, Reason: "cloud_only", VRAMKnown: true},
		{Version: 1, Action: sdk.ResourcePlanReject, Reason: "local_required", GPUDevice: "nvidia:GPU-abcdef01", VRAMKnown: true},
	} {
		if bad.Validate() == nil {
			t.Fatal("invented unmeasured capacity accepted", bad)
		}
	}
}
