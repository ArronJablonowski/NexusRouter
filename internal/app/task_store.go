package app

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

// The dispatcher owns this store and closes it after joining workers. Request
// paths borrow it; they never close it or silently replace a stale handle.
// Standalone Services retain the existing full-validation open/close behavior.
func (s *Service) taskStorage(ctx context.Context, readOnly bool) (*telemetry.Store, func(), error) {
	s.taskStoreMu.Lock()
	db := s.taskStore
	s.taskStoreMu.Unlock()
	if db != nil {
		if err := db.ValidateIdentity(ctx, s.settings.Telemetry.Database); err != nil {
			return nil, nil, err
		}
		return db, func() {}, nil
	}
	var err error
	if readOnly {
		db, err = telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	} else {
		db, err = telemetry.Open(ctx, s.settings.Telemetry.Database)
	}
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
}

func (s *Service) openTaskStore(ctx context.Context) (*telemetry.Store, func(), error) {
	return s.taskStorage(ctx, false)
}

func (s *Service) openTaskReadStore(ctx context.Context) (*telemetry.Store, func(), error) {
	return s.taskStorage(ctx, true)
}
