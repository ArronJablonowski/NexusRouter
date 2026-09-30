package sessions

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// ReplayContinuation assesses explicit reuse of durable conversation context,
// not retry authority. One bounded traversal supplies both replay and recovery
// proof. An exact model-interruption terminal may release completed messages
// for a fresh task; partial deltas remain absent, and InterruptedTurn remains
// true in the returned raw snapshot when the prior model turn was unfinished.
func ReplayContinuation(ctx context.Context, r Reader, task string) (Snapshot, ContinuationStatus, error) {
	if ctx == nil || r == nil || !ValidEventPageID(task) {
		return Snapshot{}, ContinuationStatus{}, ErrHistory
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, ContinuationStatus{}, err
	}
	capture := &continuationCapture{reader: r, remaining: 8 << 20}
	snapshot, err := Replay(ctx, capture, task)
	if err != nil {
		return Snapshot{}, ContinuationStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, ContinuationStatus{}, err
	}
	tail := capture.events
	if len(tail) > 2 {
		tail = tail[len(tail)-2:]
	}
	status := AssessContinuation(snapshot, tail)
	if snapshot.State == "failed" && len(snapshot.Pending) == 0 && !snapshot.UncertainEffects && len(capture.events) >= 2 {
		terminal := capture.events[len(capture.events)-1]
		if terminal.Kind == runtime.TaskFailed && terminal.Data.Code == "interrupted_model" {
			plan, planErr := PlanInterruptedModel([][]runtime.Event{capture.events[:len(capture.events)-1]}, terminal.Time, false)
			if planErr == nil && len(plan.Events) == 1 && reflect.DeepEqual(plan.Events[0], terminal) {
				status.HistoryEligible, status.Reason = true, "recovered_model"
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, ContinuationStatus{}, err
	}
	if status.Validate() != nil {
		return Snapshot{}, ContinuationStatus{}, ErrHistory
	}
	return snapshot, status, nil
}

type continuationCapture struct {
	reader    Reader
	events    []runtime.Event
	remaining int
}

func (c *continuationCapture) Read(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page, err := c.reader.Read(ctx, task, after, limit)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(page) > limit || len(page) > 10000-len(c.events) {
		return nil, ErrHistory
	}
	owned := make([]runtime.Event, 0, len(page))
	for _, event := range page {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := event.Encode()
		if err != nil || len(body) > c.remaining {
			return nil, ErrHistory
		}
		var copy runtime.Event
		if json.Unmarshal(body, &copy) != nil {
			return nil, ErrHistory
		}
		c.remaining -= len(body)
		owned = append(owned, copy)
	}
	c.events = append(c.events, owned...)
	return owned, nil
}
