package app

import (
	"context"
	"errors"

	"darwinrouter/runtime"
)

var ErrEventDelivery = errors.New("task event delivery failed")

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
