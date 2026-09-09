package sessions

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func eventLogFixture(position int64, id string) CommittedEvent {
	return CommittedEvent{Version: 1, Position: position, Event: runtime.Event{Version: 1, ID: id, TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: position, Time: time.Now().UTC(), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "provider", ModelID: "model"}}}
}

func TestEventLogCursorCanonicalRoundTrip(t *testing.T) {
	want := EventLogCursor{Version: 1, LastPosition: 7, LastEventID: "event-seven", HighWaterPosition: 19, HighWaterEventID: "event-nineteen"}
	encoded, err := EncodeEventLogCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseEventLogCursor(encoded)
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	for _, invalid := range []string{
		"",
		strings.Repeat("x", MaxEventLogCursorBytes+1),
		encoded + "=",
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"last_position":7,"last_event_id":"event-seven","high_water_position":19,"high_water_event_id":"event-nineteen","version":1}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1, "last_position":7,"last_event_id":"event-seven","high_water_position":19,"high_water_event_id":"event-nineteen"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"last_position":20,"last_event_id":"event-twenty","high_water_position":19,"high_water_event_id":"event-nineteen"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":2,"last_position":7,"last_event_id":"event-seven","high_water_position":19,"high_water_event_id":"event-nineteen"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"last_position":0,"last_event_id":"event-seven","high_water_position":0,"high_water_event_id":""}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"last_position":7,"last_event_id":"","high_water_position":19,"high_water_event_id":"event-nineteen"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"last_position":7,"last_event_id":"same-event","high_water_position":19,"high_water_event_id":"same-event"}`)),
	} {
		if _, err := ParseEventLogCursor(invalid); !errors.Is(err, ErrEventLog) {
			t.Fatalf("accepted noncanonical cursor %q: %v", invalid, err)
		}
	}
}

func TestEventLogEventIDCompatibilityAndSafety(t *testing.T) {
	for _, valid := range []string{"event", "event:provider:42", ":", "event with space", "event\nline", "event\x00nul"} {
		if !ValidEventLogEventID(valid) {
			t.Fatalf("rejected compatible event ID %q", valid)
		}
		cursor := EventLogCursor{Version: 1, LastPosition: 1, LastEventID: valid, HighWaterPosition: 1, HighWaterEventID: valid}
		encoded, err := EncodeEventLogCursor(cursor)
		if err != nil {
			t.Fatal(err)
		}
		if decoded, err := ParseEventLogCursor(encoded); err != nil || decoded != cursor {
			t.Fatal(decoded, err)
		}
	}
	for _, invalid := range []string{"", string([]byte{0xff}), strings.Repeat("x", 129)} {
		if ValidEventLogEventID(invalid) {
			t.Fatalf("accepted unsafe event ID %q", invalid)
		}
	}
	worst := EventLogCursor{Version: 1, LastPosition: 1, LastEventID: strings.Repeat("<", 128), HighWaterPosition: 2, HighWaterEventID: strings.Repeat(">", 128)}
	encoded, err := EncodeEventLogCursor(worst)
	if err != nil || len(encoded) > MaxEventLogCursorBytes {
		t.Fatal("maximum escaped cursor is not representable", len(encoded), err)
	}
	if decoded, err := ParseEventLogCursor(encoded); err != nil || decoded != worst {
		t.Fatal(decoded, err)
	}
}

func TestCommittedEventPageValidation(t *testing.T) {
	first := eventLogFixture(4, "event-one")
	second := eventLogFixture(5, "event-two")
	cursor, err := EncodeEventLogCursor(EventLogCursor{Version: 1, LastPosition: 5, LastEventID: "event-two", HighWaterPosition: 12, HighWaterEventID: "event-twelve"})
	if err != nil {
		t.Fatal(err)
	}
	valid := CommittedEventPage{Version: 1, Events: []CommittedEvent{first, second}, NextCursor: cursor, HasMore: true}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CommittedEventPage){
		"wrong version":   func(p *CommittedEventPage) { p.Version = 2 },
		"cursor missing":  func(p *CommittedEventPage) { p.NextCursor = "" },
		"position order":  func(p *CommittedEventPage) { p.Events[1].Position = 3 },
		"position gap":    func(p *CommittedEventPage) { p.Events[1].Position = 9 },
		"duplicate event": func(p *CommittedEventPage) { p.Events[1].Event.ID = p.Events[0].Event.ID },
		"invalid event":   func(p *CommittedEventPage) { p.Events[1].Event.TaskID = "" },
		"colon task":      func(p *CommittedEventPage) { p.Events[1].Event.TaskID = "task:invalid" },
		"colon session":   func(p *CommittedEventPage) { p.Events[1].Event.SessionID = "session:invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			page := valid
			page.Events = append([]CommittedEvent(nil), valid.Events...)
			mutate(&page)
			if !errors.Is(page.Validate(), ErrEventLog) {
				t.Fatal("invalid page accepted")
			}
		})
	}
	caughtUp := valid
	caughtUp.HasMore = false
	caughtUp.NextCursor, err = EncodeEventLogCursor(EventLogCursor{Version: 1, LastPosition: 5, LastEventID: "event-two", HighWaterPosition: 5, HighWaterEventID: "event-two"})
	if err != nil {
		t.Fatal(err)
	}
	if err := caughtUp.Validate(); err != nil {
		t.Fatal("caught-up page must retain its checkpoint cursor", err)
	}
	emptyCursor, err := EncodeEventLogCursor(EventLogCursor{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	empty := CommittedEventPage{Version: 1, NextCursor: emptyCursor}
	if err := empty.Validate(); err != nil {
		t.Fatal("empty ledger page rejected", err)
	}
	if err := (EventLogOptions{Limit: 100}).Validate(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is((EventLogOptions{Limit: 0}).Validate(), ErrEventLog) || !errors.Is((EventLogOptions{After: "invalid", Limit: 1}).Validate(), ErrEventLog) {
		t.Fatal("invalid options accepted")
	}
	tooLarge := empty
	tooLarge.Events = []CommittedEvent{eventLogFixture(1, "event-large")}
	tooLarge.Events[0].Event.Data.Text = strings.Repeat("x", MaxCommittedEventPageBytes)
	tooLarge.NextCursor, err = EncodeEventLogCursor(EventLogCursor{Version: 1, LastPosition: 1, LastEventID: "event-large", HighWaterPosition: 1, HighWaterEventID: "event-large"})
	if err != nil || !errors.Is(tooLarge.Validate(), ErrEventTooLarge) {
		t.Fatal("oversized page did not retain its public size error", err)
	}
}
