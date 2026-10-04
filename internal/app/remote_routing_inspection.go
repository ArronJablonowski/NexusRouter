package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/health"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

// RemoteRoutingInspection computes the local policy preview over a caller's
// permitted models. The remote boundary must establish model/cloud permission
// before calling this method and project only the routing fields for transport.
func (s *Service) RemoteRoutingInspection(ctx context.Context, modelIDs []string) (contract.ModelInspectionPage, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || len(modelIDs) > contract.MaxInspectionModels {
		return contract.ModelInspectionPage{}, ErrInspection
	}
	allowed := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		allowed[id] = true
	}
	report, err := s.healthReportScoped(ctx, health.Check{Component: "supervisor", Status: "unknown", Code: "supervisor_unavailable"}, allowed)
	if err != nil {
		return contract.ModelInspectionPage{}, err
	}
	return s.browserModelsScoped(ctx, report, allowed)
}
