package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type EventLogCursor = sessions.EventLogCursor
type ReadCommittedEventsOptions = sessions.EventLogOptions
type CommittedEvent = sessions.CommittedEvent
type CommittedEventPage = sessions.CommittedEventPage

var ErrEventLog = sessions.ErrEventLog
var ErrEventTooLarge = sessions.ErrEventTooLarge

var EncodeEventLogCursor = sessions.EncodeEventLogCursor
var ParseEventLogCursor = sessions.ParseEventLogCursor

// ReadCommittedEvents returns one insertion-fenced page from the database-wide
// committed runtime-event ledger. Persist NextCursor only after handling every
// event in the page; consumers provide their own at-least-once deduplication by
// Event.ID. This is bounded inspection, not a live subscription.
func (c *Client) ReadCommittedEvents(ctx context.Context, options ReadCommittedEventsOptions) (CommittedEventPage, error) {
	if !c.valid(ctx) {
		return CommittedEventPage{}, ErrAdmission
	}
	return app.ReadCommittedEvents(ctx, c.database, options)
}
