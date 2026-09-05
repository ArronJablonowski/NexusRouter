package telemetry

import "context"

// OpenWorkflowScanControl opens only an existing current-schema WAL database.
// It cannot initialize, create or migrate storage as a side effect of scanning.
func OpenWorkflowScanControl(ctx context.Context, path string) (*Store, error) {
	return openExistingControl(ctx, path, ErrWorkflowScanUnavailable)
}
