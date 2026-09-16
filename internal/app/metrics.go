package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
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
	observedAt := time.Now().UTC()
	resourceMetrics := metrics.UnavailableResources(observedAt)
	if s.settings.Mode != "cloud_only" && s.settings.Hardware.AutoProfile && s.profile != nil {
		if profile, profileErr := s.profile(ctx); profileErr == nil {
			if measured, measurementErr := metrics.ResourcesFromSnapshot(profile); measurementErr == nil {
				resourceMetrics = measured
			}
		}
	}
	if reservations, installed, reservationErr := s.hostReservationSnapshot(ctx, observedAt); installed && reservationErr == nil {
		if measured, measurementErr := metrics.WithReservationSnapshot(resourceMetrics, reservations); measurementErr == nil {
			resourceMetrics = measured
		}
	}
	if ctx.Err() != nil {
		return metrics.Snapshot{}, ErrMetrics
	}
	snapshot.ObservedAt = time.Now().UTC()
	snapshot.Resources = &resourceMetrics
	if snapshot.Validate() != nil {
		resourceMetrics = metrics.UnavailableResources(snapshot.ObservedAt)
		snapshot.Resources = &resourceMetrics
		if snapshot.Validate() != nil {
			return metrics.Snapshot{}, ErrMetrics
		}
	}
	return snapshot, nil
}
