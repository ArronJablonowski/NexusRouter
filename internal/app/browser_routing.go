package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/health"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

// BrowserRouting uses the dispatch scorer over configured and paired candidates.
// This is an admission preview, never dispatch authority or a reservation.
func (s *Service) BrowserRouting(ctx context.Context, report health.Report) (contract.ModelInspectionPage, error) {
	page, err := s.BrowserModels(ctx, report)
	if err != nil || page.Availability != contract.Available {
		return page, err
	}
	page.Rankings = s.browserRankingsUnified(ctx, &page.Models, true)
	if page.Rankings == nil || page.Validate() != nil {
		return contract.ModelInspectionPage{}, ErrInspection
	}
	return page, nil
}
