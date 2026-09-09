package sessions

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const MaxCommittedEventPageBytes = 8 << 20
const MaxEventLogCursorBytes = 4096

var ErrEventLog = errors.New("committed event log unavailable or invalid")

// EventLogCursor is an opaque position in the database-wide committed runtime
// event ledger. Positions are ordering tokens, not event IDs or task sequences.
type EventLogCursor struct {
	Version           int    `json:"version"`
	LastPosition      int64  `json:"last_position"`
	LastEventID       string `json:"last_event_id"`
	HighWaterPosition int64  `json:"high_water_position"`
	HighWaterEventID  string `json:"high_water_event_id"`
}

func (c EventLogCursor) Validate() error {
	if c.Version != 1 || c.LastPosition < 0 || c.HighWaterPosition < c.LastPosition || !eventLogAnchorValid(c.LastPosition, c.LastEventID) || !eventLogAnchorValid(c.HighWaterPosition, c.HighWaterEventID) || ((c.LastPosition == c.HighWaterPosition) != (c.LastEventID == c.HighWaterEventID)) {
		return ErrEventLog
	}
	return nil
}

func eventLogAnchorValid(position int64, eventID string) bool {
	if position == 0 {
		return eventID == ""
	}
	return position > 0 && ValidEventLogEventID(eventID)
}

// ValidEventLogEventID preserves the bounded, fully opaque event identities
// accepted by earlier journals. They are base64-wrapped inside cursors and
// never interpreted as path segments or compound delimiters.
func ValidEventLogEventID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id)
}

func EncodeEventLogCursor(cursor EventLogCursor) (string, error) {
	if cursor.Validate() != nil {
		return "", ErrEventLog
	}
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", ErrEventLog
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func ParseEventLogCursor(value string) (EventLogCursor, error) {
	if value == "" || len(value) > MaxEventLogCursorBytes {
		return EventLogCursor{}, ErrEventLog
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return EventLogCursor{}, ErrEventLog
	}
	var cursor EventLogCursor
	if json.Unmarshal(body, &cursor) != nil {
		return EventLogCursor{}, ErrEventLog
	}
	canonical, err := EncodeEventLogCursor(cursor)
	if err != nil || canonical != value {
		return EventLogCursor{}, ErrEventLog
	}
	return cursor, nil
}

type EventLogOptions struct {
	After string
	Limit int
}

func (o EventLogOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 100 {
		return ErrEventLog
	}
	if o.After != "" {
		if _, err := ParseEventLogCursor(o.After); err != nil {
			return ErrEventLog
		}
	}
	return nil
}

type CommittedEvent struct {
	Version  int           `json:"version"`
	Position int64         `json:"position"`
	Event    runtime.Event `json:"event"`
}

func (e CommittedEvent) Validate() error {
	if e.Version != 1 || e.Position < 1 || e.Event.Validate() != nil || !ValidEventLogEventID(e.Event.ID) || !ValidEventPageID(e.Event.TaskID) || !ValidEventPageID(e.Event.SessionID) {
		return ErrEventLog
	}
	return nil
}

type CommittedEventPage struct {
	Version    int              `json:"version"`
	Events     []CommittedEvent `json:"events"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

func (p CommittedEventPage) Validate() error {
	if p.Version != 1 || len(p.Events) > 100 || p.NextCursor == "" || (p.HasMore && len(p.Events) == 0) {
		return ErrEventLog
	}
	seen := make(map[string]bool, len(p.Events))
	var last int64
	for i, item := range p.Events {
		if item.Validate() != nil || i > 0 && item.Position != last+1 || seen[item.Event.ID] {
			return ErrEventLog
		}
		last = item.Position
		seen[item.Event.ID] = true
	}
	cursor, err := ParseEventLogCursor(p.NextCursor)
	if err != nil || p.HasMore != (cursor.LastPosition < cursor.HighWaterPosition) {
		return ErrEventLog
	}
	if len(p.Events) > 0 && (cursor.LastPosition != last || cursor.LastEventID != p.Events[len(p.Events)-1].Event.ID || cursor.HighWaterPosition < last) {
		return ErrEventLog
	}
	body, err := json.Marshal(p)
	if err != nil {
		return ErrEventLog
	}
	if len(body) > MaxCommittedEventPageBytes {
		return ErrEventTooLarge
	}
	return nil
}
