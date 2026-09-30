package v1

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
)

// MetricsExporter owns an explicitly started periodic exporter. The caller must
// Close it to cancel and join background work. Scheduling is not durable and
// delivery is best effort, not exactly once. Trusted secret lookup callbacks
// must return promptly; Close cannot forcibly interrupt host callback code.
type MetricsExporter struct{ exporter *app.MetricsExporter }

func (e *MetricsExporter) Close() error {
	if e == nil {
		return nil
	}
	return e.exporter.Close()
}

func (e *MetricsExporter) Health() health.Check {
	if e == nil {
		return (*app.MetricsExporter)(nil).Health()
	}
	return e.exporter.Health()
}

// StartMetricsExport starts an immediate attempt and waits interval after each
// completion before trying again. Attempts do not overlap within this handle.
// Export health never grants task authority.
func (c *Client) StartMetricsExport(ctx context.Context, options MetricsExportOptions, interval time.Duration) (*MetricsExporter, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := app.StartMetricsExport(ctx, c.service, options, interval)
	if err != nil {
		return nil, err
	}
	return &MetricsExporter{exporter: e}, nil
}
