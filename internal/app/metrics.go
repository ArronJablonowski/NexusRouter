package app

import (
	"context"
	"errors"
	"time"

	"darwinrouter/internal/telemetry"
	"darwinrouter/metrics"
)

var ErrMetrics = errors.New("metrics unavailable")

// Metrics inspects existing storage; missing storage is unavailable, not an
// empty population, and this operation cannot create or migrate a database.
func (s *Service) Metrics(ctx context.Context) (metrics.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if s == nil {
		return metrics.Snapshot{}, ErrMetrics
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return metrics.Snapshot{}, ErrMetrics
	}
	defer db.Close()
	snapshot, err := db.Metrics(ctx)
	if err != nil {
		return metrics.Snapshot{}, ErrMetrics
	}
	return snapshot, nil
}
