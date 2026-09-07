package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

func TestServiceMetricsMissingAndExisting(t *testing.T) {
	s := submissionService(t)
	ctx := context.Background()
	if _, err := s.Metrics(ctx); !errors.Is(err, ErrMetrics) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created missing storage: %v", err)
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	snapshot, err := s.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil {
		t.Fatal(snapshot, err)
	}
	if snapshot.Resources == nil {
		t.Fatal("missing explicit resource availability")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Metrics(canceled); !errors.Is(err, ErrMetrics) {
		t.Fatal(err)
	}
}

type metricsProfiler struct {
	called int
	value  resources.Measurement
	err    error
}

func (p *metricsProfiler) Measure(context.Context) (resources.Measurement, error) {
	p.called++
	return p.value, p.err
}

func TestServiceMetricsResourceObservationAndUnavailability(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		mode       string
		auto       bool
		profile    resources.Measurement
		profileErr error
		wantCalls  int
		wantCPU    bool
	}{
		{name: "observed", mode: "hybrid", auto: true, profile: resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now().UTC(), CPUs: 8, TotalRAM: 100, AvailableRAM: 50}}, wantCalls: 1, wantCPU: true},
		{name: "failed", mode: "hybrid", auto: true, profileErr: errors.New("private profiler failure"), wantCalls: 1},
		{name: "cloud only", mode: "cloud_only", auto: true},
		{name: "disabled", mode: "hybrid", auto: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := submissionService(t)
			s.settings.Mode = tc.mode
			s.settings.Hardware.AutoProfile = tc.auto
			profiler := &metricsProfiler{value: tc.profile, err: tc.profileErr}
			s.profile = func(ctx context.Context) (resources.Snapshot, error) {
				measurement, err := profiler.Measure(ctx)
				return measurement.Snapshot, err
			}
			db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			snapshot, err := s.Metrics(ctx)
			if err != nil || snapshot.Validate() != nil || profiler.called != tc.wantCalls || snapshot.Resources == nil {
				t.Fatal(snapshot, profiler.called, err)
			}
			if snapshot.Resources.Measurements[0].Available != tc.wantCPU {
				t.Fatal(snapshot.Resources)
			}
		})
	}
}
