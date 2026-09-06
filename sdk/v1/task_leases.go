package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// TaskLeaseStatus is a metadata-only observation, never permission to reclaim
// ownership, repeat an effect, or accept recovered worker output.
type TaskLeaseStatus = workers.TaskLeaseStatus

type TaskLeaseCounts = workers.TaskLeaseCounts
type TaskRecoveryCounts = workers.TaskRecoveryCounts

// InspectTaskLeases reads an existing database without migration, ownership
// probing, provider execution, or recovery mutations.
func (c *Client) InspectTaskLeases(ctx context.Context, task string) (TaskLeaseStatus, error) {
	if !c.valid(ctx) {
		return TaskLeaseStatus{Version: 1}, ErrAdmission
	}
	status, err := app.InspectTaskLeases(ctx, c.database, task)
	status.Version = 1
	return status, err
}
