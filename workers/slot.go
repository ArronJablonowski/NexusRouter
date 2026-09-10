package workers

import "context"

// WithSlot runs callback under the Supervisor's existing in-process concurrency
// bound. It is intentionally only a capacity primitive: unlike Run, it creates
// no runtime task, emits no events, and acquires no resource lease.
//
// A callback must cooperate with cancellation through ctx. WithSlot does not
// return or release the slot until callback has returned (or panicked), so a
// canceled callback cannot overlap a replacement while it is still running.
// Callback panics are contained and reported as ErrWork.
func (s *Supervisor) WithSlot(ctx context.Context, callback func(context.Context) error) (err error) {
	if s == nil || ctx == nil || callback == nil {
		return ErrWork
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.slots }()
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if recover() != nil {
			err = ErrWork
		}
	}()
	return callback(ctx)
}
