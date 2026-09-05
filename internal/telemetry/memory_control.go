package telemetry

import (
	"context"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

// OpenMemoryControl opens only existing current-schema WAL storage. Operator
// writes cannot create missing databases or silently migrate older ones.
func OpenMemoryControl(ctx context.Context, path string) (*Store, error) {
	return openExistingControl(ctx, path, memory.ErrInput)
}
