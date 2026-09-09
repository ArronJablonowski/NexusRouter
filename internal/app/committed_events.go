package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ReadCommittedEvents reads one insertion-fenced page from an existing
// database-wide runtime-event ledger. It never creates or migrates storage.
func ReadCommittedEvents(ctx context.Context, path string, options sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
	zero := sessions.CommittedEventPage{}
	if ctx == nil || path == "" || options.Validate() != nil {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failure := func(err error) (sessions.CommittedEventPage, error) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		for _, known := range []error{sessions.ErrEventLog, sessions.ErrEventTooLarge} {
			if errors.Is(err, known) {
				return zero, known
			}
		}
		return zero, ErrInspection
	}
	if ctx.Err() != nil {
		return failure(ctx.Err())
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return failure(err)
	}
	defer db.Close()
	page, err := db.ReadCommittedEventPage(ctx, options)
	if err != nil {
		return failure(err)
	}
	return page, nil
}
