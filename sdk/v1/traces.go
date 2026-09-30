package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/traces"
)

type TraceSnapshot = traces.Snapshot
type TraceExportOptions = traces.ExportOptions

func (c *Client) TraceSnapshot(ctx context.Context, limit int) (TraceSnapshot, error) {
	if !c.valid(ctx) {
		return TraceSnapshot{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return TraceSnapshot{}, err
	}
	return c.service.TraceSnapshot(ctx, limit)
}

// ExportTraces sends one bounded, content-free recent-task snapshot to an OTLP
// collector. It starts no background exporter and mutates no durable state.
func (c *Client) ExportTraces(ctx context.Context, options TraceExportOptions) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.service.ExportTraces(ctx, options)
}
