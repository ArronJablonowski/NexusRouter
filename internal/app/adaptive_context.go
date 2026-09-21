package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/contextpolicy"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// chooseContextTier freezes the context allocation before host admission. The
// advertised window remains the runtime compaction ceiling; the selected tier
// is the provider allocation for this attempt.
func chooseContextTier(ctx context.Context, model config.Model, request Request, estimated int, evidence []contextpolicy.Evidence, explore bool, minimumSamples int) (int, error) {
	if request.ContextTokens > 0 {
		if request.ContextTokens > model.ContextTokens {
			return 0, ErrAdmission
		}
		return request.ContextTokens, nil
	}
	if estimated < 1 {
		messages := append([]providers.Message(nil), request.Messages...)
		if request.Prompt != "" {
			messages = append(messages, providers.Message{Role: "user", Content: request.Prompt})
		}
		var err error
		estimated, err = providers.EstimateWith(ctx, request.contextEstimator, providers.Request{Model: model.Model, Messages: messages})
		if err != nil {
			return 0, ErrAdmission
		}
	}
	tier := contextpolicy.Select(contextpolicy.Request{
		AdvertisedMaximum: model.ContextTokens,
		WorkingTier:       model.WorkingContextTokens(),
		EstimatedTokens:   estimated,
		Evidence:          evidence,
		Explore:           explore,
		MinimumSamples:    minimumSamples,
	})
	if tier < estimated || tier < 1 {
		return 0, ErrAdmission
	}
	return tier, nil
}
