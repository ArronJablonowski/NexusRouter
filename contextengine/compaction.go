package contextengine

import (
	"context"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func freezeCompaction(source sessions.Snapshot, request sessions.CompactionRequest) (sessions.Snapshot, sessions.CompactionRequest, error) {
	if !sessions.ValidEventPageID(source.TaskID) || source.Sequence < 1 || source.State != "completed" || source.InterruptedTurn || source.UncertainEffects || len(source.Pending) > 0 || len(source.MessageSequences) > maxBytes/8 || !validBundles(source.Messages) || sessions.ValidateCompactionRequest(&request) != nil {
		return sessions.Snapshot{}, sessions.CompactionRequest{}, ErrEngine
	}
	minimal := sessions.Snapshot{TaskID: source.TaskID, State: source.State, Sequence: source.Sequence, Messages: source.Messages, MessageSequences: source.MessageSequences}
	var frozen struct {
		Source  sessions.Snapshot
		Request sessions.CompactionRequest
	}
	if cloneBounded(struct {
		Source  sessions.Snapshot
		Request sessions.CompactionRequest
	}{minimal, request}, &frozen) != nil {
		return sessions.Snapshot{}, sessions.CompactionRequest{}, ErrEngine
	}
	if _, _, err := sessions.PrepareContinuation(frozen.Source, frozen.Request); err != nil {
		return sessions.Snapshot{}, sessions.CompactionRequest{}, ErrEngine
	}
	return frozen.Source, frozen.Request, nil
}

// SelectCompaction allows retaining more recent context, never altering an
// operator-reviewed summary or dropping additional required recent messages.
func SelectCompaction(ctx context.Context, engine Engine, source sessions.Snapshot, request sessions.CompactionRequest) (out sessions.CompactionRequest, err error) {
	defer func() {
		if recover() != nil {
			out = sessions.CompactionRequest{}
			err = ErrEngine
		}
	}()
	if ctx == nil || ctx.Err() != nil {
		return out, ErrEngine
	}
	engine, err = chosenEngine(engine)
	if err != nil {
		return out, ErrEngine
	}
	pristine, original, err := freezeCompaction(source, request)
	if err != nil {
		return out, ErrEngine
	}
	callback, callbackRequest, err := freezeCompaction(pristine, original)
	if err != nil {
		return out, ErrEngine
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	selected, err := engine.PrepareCompaction(bounded, callback, callbackRequest)
	if err != nil || bounded.Err() != nil || selected.Keep < original.Keep || sessions.ValidateCompactionRequest(&selected) != nil || !reflect.DeepEqual(selected.Summary, original.Summary) {
		return sessions.CompactionRequest{}, ErrEngine
	}
	if cloneBounded(selected, &out) != nil {
		return sessions.CompactionRequest{}, ErrEngine
	}
	if _, _, err := sessions.PrepareContinuation(pristine, out); err != nil || bounded.Err() != nil {
		return sessions.CompactionRequest{}, ErrEngine
	}
	return out, nil
}

// Compact materializes only canonical host-owned content after selection.
func Compact(ctx context.Context, engine Engine, source sessions.Snapshot, request sessions.CompactionRequest) ([]providers.Message, *runtime.ContextCompaction, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrEngine
	}
	frozen, original, err := freezeCompaction(source, request)
	if err != nil {
		return nil, nil, ErrEngine
	}
	selected, err := SelectCompaction(ctx, engine, frozen, original)
	if err != nil {
		return nil, nil, ErrEngine
	}
	messages, record, err := sessions.PrepareContinuation(frozen, selected)
	if err != nil || ctx.Err() != nil || !validBundles(messages) {
		return nil, nil, ErrEngine
	}
	var owned struct {
		Messages []providers.Message
		Record   *runtime.ContextCompaction
	}
	if cloneBounded(struct {
		Messages []providers.Message
		Record   *runtime.ContextCompaction
	}{messages, record}, &owned) != nil || ctx.Err() != nil {
		return nil, nil, ErrEngine
	}
	return owned.Messages, owned.Record, nil
}
