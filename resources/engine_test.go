package resources

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type engineProfiler func(context.Context) (Measurement, error)

func (f engineProfiler) Measure(ctx context.Context) (Measurement, error) { return f(ctx) }

func TestMeasureSnapshotDetachedAndUnknownFactsPreserved(t *testing.T) {
	source := deviceSnapshot(time.Now())
	source.Source = "private profiler marker"
	swap, ram, free := uint64(2), uint64(100), uint64(50)
	thermal := false
	source.SwapUsed = &swap
	source.VRAMTotal = &ram
	source.VRAMAvailable = &free
	source.ThermalPressure = &thermal
	out, err := MeasureSnapshot(context.Background(), engineProfiler(func(context.Context) (Measurement, error) { return Measurement{Version: 1, Snapshot: source}, nil }))
	if err != nil || out.Source != "embedded-resource-profiler" {
		t.Fatal(out, err)
	}
	*out.SwapUsed = 99
	*out.VRAMTotal = 99
	*out.VRAMAvailable = 99
	*out.ThermalPressure = true
	out.GPUs.Sources[0].Devices[0].AvailableBytes = 0
	out.GPUs.Sources[0].Status = "changed"
	if swap != 2 || ram != 100 || free != 50 || thermal || source.GPUs.Sources[0].Devices[0].AvailableBytes != 100 || source.GPUs.Sources[0].Status != "observed" {
		t.Fatal("aliased original")
	}
	empty, err := MeasureSnapshot(context.Background(), engineProfiler(func(context.Context) (Measurement, error) { return Measurement{Version: 1}, nil }))
	if err != nil || empty.GPUs != nil || empty.VRAMTotal != nil || !empty.Time.IsZero() || empty.TotalRAM != 0 {
		t.Fatal("invented facts", empty, err)
	}
}

func TestMeasureSnapshotRejectsInvalidVersionsErrorsAndMetadata(t *testing.T) {
	for _, mode := range []string{"version", "error", "panic", "source", "sources", "devices", "gpu_text", "thermal_text", "thermal_case", "thermal_missing", "thermal_conflict", "typednil", "nil"} {
		t.Run(mode, func(t *testing.T) {
			p := engineProfiler(func(context.Context) (Measurement, error) {
				s := deviceSnapshot(time.Now())
				switch mode {
				case "version":
					return Measurement{Version: 2, Snapshot: s}, nil
				case "error":
					return Measurement{Version: 1, Snapshot: s}, errors.New("private error")
				case "panic":
					panic("private panic")
				case "source":
					s.Source = strings.Repeat("x", 129)
				case "sources":
					s.GPUs.Sources = append(s.GPUs.Sources, s.GPUs.Sources[0], s.GPUs.Sources[0])
				case "devices":
					s.GPUs.Sources[0].Devices = make([]GPUDevice, 33)
				case "gpu_text":
					s.GPUs.Sources[0].Devices[0].ID = "private\x1b[31m"
				case "thermal_text":
					s.ThermalState = "private thermal data"
				case "thermal_case":
					pressure := false
					s.ThermalState, s.ThermalPressure = "Nominal", &pressure
				case "thermal_missing":
					s.ThermalState = "nominal"
				case "thermal_conflict":
					pressure := false
					s.ThermalState, s.ThermalPressure = "critical", &pressure
				}
				return Measurement{Version: 1, Snapshot: s}, nil
			})
			var profiler Profiler = p
			if mode == "typednil" {
				var typed engineProfiler
				profiler = typed
			}
			if mode == "nil" {
				profiler = nil
			}
			out, err := MeasureSnapshot(context.Background(), profiler)
			if !reflect.DeepEqual(out, Snapshot{}) || !errors.Is(err, ErrProfile) || strings.Contains(err.Error(), "private") {
				t.Fatal(out, err)
			}
		})
	}
}

func TestMeasureSnapshotCancellationAndNilContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := engineProfiler(func(context.Context) (Measurement, error) {
		t.Fatal("called after canceled")
		return Measurement{}, nil
	})
	if _, err := MeasureSnapshot(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := MeasureSnapshot(nil, p); !errors.Is(err, ErrProfile) {
		t.Fatal(err)
	}
	p = func(context.Context) (Measurement, error) {
		return Measurement{}, errors.Join(errors.New("private"), context.DeadlineExceeded)
	}
	if out, err := MeasureSnapshot(context.Background(), p); !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(out, Snapshot{}) || strings.Contains(err.Error(), "private") {
		t.Fatal(out, err)
	}
	var nilctx context.Context
	if _, err := (HostProfiler{}).Measure(nilctx); err == nil {
		t.Fatal("nil context accepted")
	}
	active, stop := context.WithCancel(context.Background())
	p = func(context.Context) (Measurement, error) {
		stop()
		return Measurement{Version: 1, Snapshot: Snapshot{TotalRAM: 123}}, nil
	}
	if out, err := MeasureSnapshot(active, p); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(out, Snapshot{}) {
		t.Fatal("returned canceled measurement", out, err)
	}
}
