package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type OutcomeRollbackReceipt = skills.OutcomeRollbackReceipt

// OutcomeRollbackOnce explicitly authorizes an operation-keyed outcome-based
// rollback within configured policy. Exact retries acknowledge the saved receipt
// rather than selecting a new cohort or repeating a catalog mutation.
func (c *Client) OutcomeRollbackOnce(ctx context.Context, operation string, expected skills.ActivationState, request skills.ComparisonSelectionRequest) (OutcomeRollbackReceipt, error) {
	if !c.valid(ctx) {
		return OutcomeRollbackReceipt{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	return c.service.OutcomeRollbackOnce(ctx, operation, expected, request)
}

// OutcomeRollbackOperation inspects an existing receipt without creating a
// catalog or granting permission for a new mutation. It is historical evidence,
// not proof that its resulting activation is still current.
func (c *Client) OutcomeRollbackOperation(ctx context.Context, key skills.Key, operation string) (OutcomeRollbackReceipt, error) {
	if !c.valid(ctx) {
		return OutcomeRollbackReceipt{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return OutcomeRollbackReceipt{}, err
	}
	return c.service.OutcomeRollbackOperation(ctx, key, operation)
}
