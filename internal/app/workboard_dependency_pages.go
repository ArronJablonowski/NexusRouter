package app

import (
	"context"
	"errors"

	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func (b *WorkboardBridge) NativeDependencies(ctx context.Context, boardID, cardID string, options contract.DependencyOptions) (contract.DependencyPage, error) {
	return b.dependenciesPage(nativeWorkboardContext(ctx), boardID, cardID, options)
}

func (b *WorkboardBridge) BrowserDependencies(ctx context.Context, subject, boardID, cardID string, options contract.DependencyOptions) (contract.DependencyPage, error) {
	trusted, err := browserWorkboardContext(ctx, subject, b.browserAuthority)
	if err != nil {
		return contract.DependencyPage{}, err
	}
	return b.dependenciesPage(trusted, boardID, cardID, options)
}

func (b *WorkboardBridge) dependenciesPage(ctx context.Context, boardID, cardID string, options contract.DependencyOptions) (contract.DependencyPage, error) {
	if b == nil || b.dependencies == nil || options.Validate() != nil {
		return contract.DependencyPage{}, errors.New("workboard dependencies unavailable")
	}
	direction := workboard.DependencyPrerequisites
	if options.Direction == contract.DependencyDependents {
		direction = workboard.DependencyDependents
	}
	page, err := b.dependencies.List(ctx, boardID, cardID, workboard.DependencyOptions{After: options.After, Limit: options.Limit, Direction: direction})
	if err != nil {
		return contract.DependencyPage{}, err
	}
	result := contract.DependencyPage{Version: contract.ContractVersion, BoardID: page.BoardID, CardID: page.CardID,
		Direction: options.Direction, GraphRevision: page.GraphRevision, GraphDigest: page.GraphDigest,
		Items: make([]contract.DependencyLink, len(page.Items)), NextCursor: page.NextCursor, HasMore: page.HasMore}
	for index, item := range page.Items {
		result.Items[index] = contract.DependencyLink{Version: contract.ContractVersion, BoardID: item.BoardID,
			CardID: item.CardID, DependencyID: item.DependencyID}
	}
	if page.Validate() != nil || result.Validate() != nil {
		return contract.DependencyPage{}, errors.New("invalid dependency projection")
	}
	return result, nil
}
