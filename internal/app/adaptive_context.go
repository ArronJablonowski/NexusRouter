package app

import (
	"context"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"math/big"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/contextpolicy"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// chooseContextTier freezes the context allocation before host admission. The
// advertised window remains the runtime compaction ceiling; the selected tier
// is the provider allocation for this attempt.
func chooseContextTier(ctx context.Context, model config.Model, request Request, estimated int, evidence []contextpolicy.Evidence, explore bool, minimumSamples int, fitsMemory func(int) bool) (int, error) {
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
		FitsMemory:        fitsMemory,
	})
	if tier < estimated || tier < 1 {
		return 0, ErrAdmission
	}
	return tier, nil
}

// contextReservationModel conservatively scales the working-tier memory
// reservation with context growth. Until provider-specific KV measurements are
// available, scaling the entire estimate over-reserves weights rather than
// pretending a 128K allocation has the same cost as 32K. Never discount below
// the configured baseline, and fail closed on overflow.
func contextReservationModel(model config.Model, tokens int) (config.Model, error) {
	working := model.WorkingContextTokens()
	if tokens <= working || tokens == 0 || model.Locality != "local" {
		return model, nil
	}
	if working < 1 || tokens > model.ContextTokens {
		return config.Model{}, ErrAdmission
	}
	scale := func(n uint64) (uint64, bool) {
		v := new(big.Int).SetUint64(n)
		v.Mul(v, big.NewInt(int64(tokens)))
		v.Add(v, big.NewInt(int64(working-1)))
		v.Quo(v, big.NewInt(int64(working)))
		return v.Uint64(), v.IsUint64()
	}
	var ok bool
	model.RAMBytes, ok = scale(model.RAMBytes)
	if !ok {
		return config.Model{}, ErrAdmission
	}
	model.VRAMBytes, ok = scale(model.VRAMBytes)
	if !ok {
		return config.Model{}, ErrAdmission
	}
	return model, nil
}

func (s *Service) contextFitsMemory(ctx context.Context, model config.Model) func(int) bool {
	if model.Locality != "local" {
		return nil
	}
	return func(tokens int) bool {
		// Baseline admission owns residency replacement. Rejecting it here
		// would prevent that path from unloading a previous resident model.
		if tokens <= model.WorkingContextTokens() {
			return true
		}
		// Use fresh measurements and the same local budget used by reservation.
		// Admission repeats this check and acquires the durable host reservation.
		sized, err := contextReservationModel(model, tokens)
		if err != nil {
			return false
		}
		if s.lockResources(ctx) != nil {
			return false
		}
		defer s.mu.Unlock()
		snapshot, err := s.resourceProfile(ctx)
		if err != nil {
			return false
		}
		plan, err := s.budget.Plan(ctx, resources.CapacityRequest{Version: resources.CapacityContractVersion, Need: modelResources(sized), Snapshot: snapshot, Now: time.Now()})
		return err == nil && plan.Action == resources.CapacityAdmit
	}
}
