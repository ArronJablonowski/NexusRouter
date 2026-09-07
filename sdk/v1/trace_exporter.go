package v1

import (
	"context"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

// TraceExporter owns an explicitly started periodic exporter. Close cancels
// and joins it. Scheduling and delivery are non-durable and best effort.
type TraceExporter struct{ exporter *app.TraceExporter }

func (e *TraceExporter) Close() error {
	if e == nil {
		return nil
	}
	return e.exporter.Close()
}

func (e *TraceExporter) Health() health.Check {
	if e == nil {
		return (*app.TraceExporter)(nil).Health()
	}
	return e.exporter.Health()
}

func (c *Client) StartTraceExport(ctx context.Context, options TraceExportOptions, interval time.Duration) (*TraceExporter, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exporter, err := app.StartTraceExport(ctx, c.service, options, interval)
	if err != nil {
		return nil, err
	}
	return &TraceExporter{exporter: exporter}, nil
}
