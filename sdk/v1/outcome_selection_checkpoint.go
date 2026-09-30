package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

type OutcomeSelectionCheckpoint = skills.OutcomeSelectionCheckpoint

// OutcomeSelectionCheckpoint inspects frozen selection evidence. A checkpoint
// is not a completion receipt or proof of current activation; inspection does
// not resume finalization, select new evidence, or create a catalog.
func (c *Client) OutcomeSelectionCheckpoint(ctx context.Context, key skills.Key, operation string) (OutcomeSelectionCheckpoint, error) {
	if !c.valid(ctx) {
		return OutcomeSelectionCheckpoint{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return OutcomeSelectionCheckpoint{}, err
	}
	return c.service.OutcomeSelectionCheckpoint(ctx, key, operation)
}
