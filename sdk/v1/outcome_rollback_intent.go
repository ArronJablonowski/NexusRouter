package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type OutcomeRollbackIntent = skills.OutcomeRollbackIntent

// OutcomeRollbackIntent inspects an existing durable selection claim. It does
// not resume an interrupted selector, authorize another attempt, or establish
// that a rollback completed. Inspection never creates a catalog.
func (c *Client) OutcomeRollbackIntent(ctx context.Context, key skills.Key, operation string) (OutcomeRollbackIntent, error) {
	if !c.valid(ctx) {
		return OutcomeRollbackIntent{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return OutcomeRollbackIntent{}, err
	}
	return c.service.OutcomeRollbackIntent(ctx, key, operation)
}
