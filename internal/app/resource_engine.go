package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

// NewServiceWithProfiler explicitly replaces measurement, not admission or
// resource accounting. The engine is trusted code and must honor cancellation.
// An injected engine is a manual measurement source when auto_profile is false.
func NewServiceWithProfiler(settings config.Settings, secret func(string) string, profiler resources.Profiler) (*Service, error) {
	svc, err := NewService(settings, secret)
	if err != nil {
		return nil, err
	}
	if profiler != nil {
		svc.profile = func(ctx context.Context) (resources.Snapshot, error) {
			return resources.MeasureSnapshot(ctx, profiler)
		}
	}
	return svc, nil
}
