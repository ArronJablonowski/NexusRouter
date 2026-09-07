package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/routing"
)

type ConfiguredModel = routing.ConfiguredModel
type ModelCatalog = routing.ModelCatalog

// ConfiguredModelCatalog returns declared routing metadata only. It does not
// discover installed models, check health/capacity, or authorize execution.
func (c *Client) ConfiguredModelCatalog(ctx context.Context) (ModelCatalog, error) {
	if !c.valid(ctx) {
		return ModelCatalog{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return ModelCatalog{}, err
	}
	return c.service.ConfiguredModelCatalog(ctx)
}
