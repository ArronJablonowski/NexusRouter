package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
)

type MetricsExportOptions = metrics.ExportOptions

// ExportMetrics explicitly sends one aggregate snapshot to the configured
// collector endpoint. It starts no background exporter and owns no persistent
// handle. Credentials are resolved through the client's secret lookup only.
func (c *Client) ExportMetrics(ctx context.Context, options MetricsExportOptions) error {
	if !c.valid(ctx) {
		return ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.service.ExportMetrics(ctx, options)
}
