package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"darwinrouter/internal/telemetry"
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
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Metrics(canceled); !errors.Is(err, ErrMetrics) {
		t.Fatal(err)
	}
}
