package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// AdvanceWorkflowScanGuarded admits each exact page before persistence or replay
// return. The trusted host guard must be read-only and cooperative: it runs under
// the transaction lock with a three-second deadline and is never abandoned.
// Guard authority is not persisted and must be re-established on every retry.
func (s *Store) AdvanceWorkflowScanGuarded(ctx context.Context, scope, name, domain string, expectedRevision int64, scanLimit int, guard func(context.Context, skills.WorkflowScanPage) error) (skills.WorkflowScanPage, error) {
	if guard == nil {
		return skills.WorkflowScanPage{}, ErrWorkflowScanUnavailable
	}
	return s.advanceWorkflowScan(ctx, scope, name, domain, expectedRevision, scanLimit, guard)
}

func guardWorkflowScan(ctx context.Context, guard func(context.Context, skills.WorkflowScanPage) error, page skills.WorkflowScanPage) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if guard == nil {
		return nil
	}
	if page.Validate() != nil {
		return ErrWorkflowScanUnavailable
	}
	body, err := json.Marshal(page)
	if err != nil || len(body) > 65536 {
		return ErrWorkflowScanUnavailable
	}
	var owned skills.WorkflowScanPage
	if json.Unmarshal(body, &owned) != nil {
		return ErrWorkflowScanUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			err = ErrWorkflowScanUnavailable
		}
		if bounded.Err() != nil {
			err = ErrWorkflowScanUnavailable
		}
	}()
	if err = guard(bounded, owned); err != nil {
		return ErrWorkflowScanUnavailable
	}
	return nil
}
