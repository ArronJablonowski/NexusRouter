package app

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

var ErrEventDelivery = errors.New("task event delivery failed")

// RunLiveStream combines committed lifecycle events and provisional redacted
// text. The event callback precedes text from the same committed marker. Both
// callbacks are synchronous, must cooperate with cancellation, and share one
// failure gate. Failure cancels and joins execution while durable cleanup
// continues without further callbacks. Text is not replayable or acceptance
// evidence; only a successful return establishes task completion.
func (s *Service) RunLiveStream(ctx context.Context, r Request, emitEvent func(runtime.Event) error, emitText func(string) error) (Result, error) {
	if ctx == nil || emitEvent == nil || emitText == nil {
		return Result{}, ErrAdmission
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var deliveryErr error
	deliver := func(callback func() error) {
		if deliveryErr != nil || ctx.Err() != nil {
			return
		}
		defer func() {
			if recover() != nil {
				deliveryErr = ErrEventDelivery
				cancel()
			}
		}()
		if callback() != nil {
			deliveryErr = ErrEventDelivery
			cancel()
		}
	}
	r.eventSink = func(e runtime.Event) { deliver(func() error { return emitEvent(e) }) }
	r.textSink = func(text string) {
		deliver(func() error {
			if !utf8.ValidString(text) {
				return ErrEventDelivery
			}
			return emitText(text)
		})
	}
	result, err := s.Run(ctx, r)
	return result, errors.Join(err, deliveryErr, ctx.Err())
}

// RunStream delivers committed, redacted events synchronously in journal order.
// Delivery failure cancels execution but never reclassifies a committed append
// as a persistence failure. Cleanup continues durably without further delivery.
func (s *Service) RunStream(ctx context.Context, r Request, emit func(runtime.Event) error) (Result, error) {
	if emit == nil {
		return Result{}, ErrAdmission
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var deliveryErr error
	r.eventSink = func(e runtime.Event) {
		if deliveryErr != nil {
			return
		}
		defer func() {
			if recover() != nil {
				deliveryErr = ErrEventDelivery
				cancel()
			}
		}()
		if emit(e) != nil {
			deliveryErr = ErrEventDelivery
			cancel()
		}
	}
	result, err := s.Run(ctx, r)
	return result, errors.Join(err, deliveryErr)
}
