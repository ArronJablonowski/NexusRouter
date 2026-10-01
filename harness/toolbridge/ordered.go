package toolbridge

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// NewOrdered accepts concurrent native arrivals but invokes in host Register
// order. Register each verified model proposal in canonical order before releasing
// it to the child. Waiting for a predecessor never starts it: the native client
// must request every call. Missing/canceled predecessors therefore cannot cause
// speculative effects, and the normal timeout bounds the entire rendezvous.
func NewOrdered(ctx context.Context, limit int, timeout time.Duration, invoke Invoke) (*Bridge, error) {
	b, e := New(ctx, limit, timeout, invoke)
	if e != nil {
		return nil, e
	}
	b.ordered = true
	return b, nil
}

func (b *Bridge) invokeInOrder(ctx context.Context, e *entry) (runtime.ToolResult, error) {
	if e.before != nil {
		select {
		case <-e.before:
		case <-ctx.Done():
		}
		// A canceled waiting call leaves a gap in canonical order. Fence the whole
		// run rather than allowing a later arrival to skip that proposal or retry it.
		if ctx.Err() != nil {
			b.mu.Lock()
			b.halted = true
			b.mu.Unlock()
			return runtime.ToolResult{}, ErrUncertain
		}
	}
	result, err := b.invokeRegistered(ctx, e.execution)
	if b.ordered && err != nil {
		// Even the first call can be canceled before acquiring the execution slot.
		// Its completed rendezvous must not let successors skip a missing effect.
		b.mu.Lock()
		b.halted = true
		b.mu.Unlock()
	}
	return result, err
}
