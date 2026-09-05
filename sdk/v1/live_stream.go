package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// RunLiveStream combines committed lifecycle events with provisional redacted
// assistant text. Events precede text from the same marker. Both callbacks are
// synchronous; either failure cancels execution and stops both delivery paths.
// Token text is not replayable, and is not accepted output until success.
func (c *Client) RunLiveStream(ctx context.Context, r Request, emitEvent func(runtime.Event) error, emitText func(string) error) (Result, error) {
	if !c.valid(ctx) || r.Version != 1 || emitEvent == nil || emitText == nil {
		return Result{Version: 1}, ErrAdmission
	}
	out, err := c.service.RunLiveStream(ctx, r.internal(), emitEvent, emitText)
	return publicResult(out), err
}
