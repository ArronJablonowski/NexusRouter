package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// RemoteCommittedLogs deliberately exports full canonical private session data.
// Only the explicitly authorized instance-wide remote logs endpoint calls this.
func (s *Service) RemoteCommittedLogs(ctx context.Context, options sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
	if s == nil || ctx == nil || options.Validate() != nil {
		return sessions.CommittedEventPage{}, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return sessions.CommittedEventPage{}, err
	}
	defer db.Close()
	return db.ReadCommittedEventPage(ctx, options)
}
