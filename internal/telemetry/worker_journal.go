package telemetry

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// AppendLeased implements the supervisor journal for non-submitted work. Hosts
// binding submission ownership use AppendWorker with the submission credentials.
func (s *Store) AppendLeased(ctx context.Context, expected int64, event runtime.Event, token, owner string) error {
	return s.AppendWorker(ctx, expected, event, token, owner, "", "")
}
