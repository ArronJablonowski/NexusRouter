package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type pointerSink struct{}

func (*pointerSink) Emit(context.Context, runtime.Event) error { return nil }

func deliveryFixtureEvent() runtime.Event {
	return runtime.Event{
		Version: 1, ID: "event", TaskID: "task", SessionID: "session",
		CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "owned"}}},
	}
}

func TestEventDeliveryCommitsBeforeDetachedOrderedFanout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := false
	configuredCalls, perCallCalls := 0, 0
	delivery := newEventDelivery(cancel, runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
		if !committed {
			t.Fatal("configured sink ran before commit")
		}
		configuredCalls++
		event.Data.Messages[0].Content = "configured mutation"
		return nil
	}), func(event runtime.Event) {
		if !committed || event.Data.Messages[0].Content != "owned" {
			t.Fatal("per-call sink did not receive detached committed projection", event)
		}
		perCallCalls++
		event.Data.Messages[0].Content = "per-call mutation"
	})
	event := deliveryFixtureEvent()
	if err := delivery.CommitAndDeliver(ctx, event, true, func() error { committed = true; return nil }); err != nil || delivery.Err() != nil {
		t.Fatal(err, delivery.Err())
	}
	if configuredCalls != 1 || perCallCalls != 1 || event.Data.Messages[0].Content != "owned" {
		t.Fatal(configuredCalls, perCallCalls, event)
	}
}

func TestEventDeliveryFailureIsSanitizedAndNeverReclassifiesCommit(t *testing.T) {
	for name, sink := range map[string]runtime.EventSink{
		"error": runtime.EventSinkFunc(func(context.Context, runtime.Event) error { return errors.New("private marker") }),
		"panic": runtime.EventSinkFunc(func(context.Context, runtime.Event) error { panic("private marker") }),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			perCall, commits := 0, 0
			delivery := newEventDelivery(cancel, sink, func(runtime.Event) { perCall++ })
			commit := func() error { commits++; return nil }
			if err := delivery.CommitAndDeliver(ctx, deliveryFixtureEvent(), true, commit); err != nil {
				t.Fatal("post-commit delivery escaped as persistence error", err)
			}
			if !errors.Is(delivery.Err(), ErrEventDelivery) || strings.Contains(delivery.Err().Error(), "private marker") || ctx.Err() == nil || perCall != 1 || commits != 1 {
				t.Fatal(delivery.Err(), ctx.Err(), perCall, commits)
			}
			if err := delivery.CommitAndDeliver(context.WithoutCancel(ctx), deliveryFixtureEvent(), true, commit); err != nil || perCall != 1 || commits != 2 {
				t.Fatal("failed delivery was retried or blocked later durability", err, perCall, commits)
			}
		})
	}
}

func TestEventDeliveryObservesTerminalContextButNotFailedCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	delivery := newEventDelivery(cancel, runtime.EventSinkFunc(func(callback context.Context, _ runtime.Event) error {
		if callback.Err() != nil {
			t.Fatal("terminal delivery received canceled append context")
		}
		calls++
		return nil
	}), nil)
	if err := delivery.CommitAndDeliver(ctx, deliveryFixtureEvent(), false, func() error { return errors.New("durability") }); err == nil || calls != 0 {
		t.Fatal("failed commit reached sink", err, calls)
	}
	cancel()
	if err := delivery.CommitAndDeliver(context.WithoutCancel(ctx), deliveryFixtureEvent(), false, func() error { return nil }); err != nil || calls != 1 || delivery.Err() != nil {
		t.Fatal("terminal event was not delivered", err, calls, delivery.Err())
	}
}

func TestInstallEventSinkRejectsTypedNil(t *testing.T) {
	var sink *pointerSink
	if validEventSink(sink) || installEventSink(&Service{}, sink) == nil {
		t.Fatal("typed-nil sink admitted")
	}
	if !validEventSink(nil) || installEventSink(&Service{}, nil) != nil {
		t.Fatal("absent sink rejected")
	}
}
