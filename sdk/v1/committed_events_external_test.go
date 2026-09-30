package v1_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

type committedEventReader interface {
	ReadCommittedEvents(context.Context, sdk.ReadCommittedEventsOptions) (sdk.CommittedEventPage, error)
}

func TestSDKCommittedEventLedgerRestartAndLaterPolling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database := filepath.Join(t.TempDir(), "committed-events.db")
	client, _ := newSDKEventSinkClient(t, database, 1, nil, sdkEventSinkSuccessProvider(), nil)
	firstResult, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.ReadCommittedEvents(ctx, sdk.ReadCommittedEventsOptions{Limit: 100})
	if err != nil || first.Validate() != nil || first.HasMore || first.NextCursor == "" || len(first.Events) == 0 {
		t.Fatal(first, err)
	}
	firstIDs := make([]string, len(first.Events))
	for i, item := range first.Events {
		if item.Event.TaskID != firstResult.TaskID || (i > 0 && item.Position <= first.Events[i-1].Position) {
			t.Fatal("first page order or task identity", item)
		}
		firstIDs[i] = item.Event.ID
	}
	first.Events[0].Event.Data.Code = "caller mutation"

	restarted, _ := newSDKEventSinkClient(t, database, 1, nil, sdkEventSinkSuccessProvider(), nil)
	secondResult, err := restarted.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := restarted.ReadCommittedEvents(ctx, sdk.ReadCommittedEventsOptions{After: first.NextCursor, Limit: 100})
	if err != nil || later.Validate() != nil || later.HasMore || later.NextCursor == first.NextCursor || len(later.Events) == 0 {
		t.Fatal(later, err)
	}
	seen := make(map[string]bool, len(firstIDs)+len(later.Events))
	for _, id := range firstIDs {
		seen[id] = true
	}
	lastPosition := first.Events[len(first.Events)-1].Position
	for _, item := range later.Events {
		if item.Position <= lastPosition || item.Event.TaskID != secondResult.TaskID || seen[item.Event.ID] {
			t.Fatal("later poll duplicated or skipped its boundary", item, lastPosition)
		}
		lastPosition = item.Position
		seen[item.Event.ID] = true
	}
	reloaded, err := restarted.ReadCommittedEvents(ctx, sdk.ReadCommittedEventsOptions{Limit: 100})
	if err != nil || len(reloaded.Events) < len(firstIDs) {
		t.Fatal(reloaded, err)
	}
	for i, id := range firstIDs {
		if reloaded.Events[i].Event.ID != id || reloaded.Events[i].Event.Data.Code == "caller mutation" {
			t.Fatal("caller mutation changed durable event ownership", reloaded.Events[i])
		}
	}
}

var _ committedEventReader = (*sdk.Client)(nil)

func TestCommittedEventLedgerPublicContractCompiles(t *testing.T) {
	if sdk.ErrEventTooLarge == nil || sdk.ErrEventTooLarge.Error() == "" {
		t.Fatal("SDK event size error alias changed")
	}
	want := sdk.EventLogCursor{Version: 1, LastPosition: 3, LastEventID: "event-three", HighWaterPosition: 8, HighWaterEventID: "event-eight"}
	encoded, err := sdk.EncodeEventLogCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sdk.ParseEventLogCursor(encoded)
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	if err := (sdk.ReadCommittedEventsOptions{After: encoded, Limit: 100}).Validate(); err != nil {
		t.Fatal(err)
	}
	caughtUpCursor, err := sdk.EncodeEventLogCursor(sdk.EventLogCursor{Version: 1, LastPosition: 3, LastEventID: "event-three", HighWaterPosition: 3, HighWaterEventID: "event-three"})
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "event-three", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	page := sdk.CommittedEventPage{Version: 1, Events: []sdk.CommittedEvent{{Version: 1, Position: 3, Event: event}}, NextCursor: caughtUpCursor}
	if err := page.Validate(); err != nil || page.HasMore {
		t.Fatal("caught-up nonempty page contract", err)
	}
}
