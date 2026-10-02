package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// warmMemoryEstimate never credits provider-reported bytes to host availability.
// It selects an explicitly qualified incremental estimate on a dedicated,
// serial, single-model provider. Shared servers and unverified observations
// retain the complete cold estimate. Only the coordinated native path calls it.
func (s *Service) warmMemoryEstimate(ctx context.Context, model config.Model, tokens int) (uint64, time.Time) {
	if ctx == nil || ctx.Err() != nil || s.providerFactory != nil || model.WarmRAMBytes == 0 || tokens != model.WorkingContextTokens() {
		return 0, time.Time{}
	}
	// Harness overhead/context expansion must never be discounted with a
	// native-model-only qualification.
	matched := false
	for _, base := range s.settings.Models {
		if base.ID == model.ID && base.RAMBytes == model.RAMBytes && base.VRAMBytes == model.VRAMBytes {
			matched = true
		}
	}
	if !matched {
		return 0, time.Time{}
	}
	for _, p := range s.settings.Providers {
		if p.ID != model.Provider || !p.DedicatedWarmMemory {
			continue
		}
		key := ""
		if p.APIKeyEnv != "" {
			if s.secret == nil {
				return 0, time.Time{}
			}
			key = s.secret(p.APIKeyEnv)
			if key == "" {
				return 0, time.Time{}
			}
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		transport, err := policy.NewTransport(true, []string{p.ResolvedEndpoint()})
		if err != nil {
			return 0, time.Time{}
		}
		defer transport.CloseIdleConnections()
		adapter, err := providers.NewHTTPWithTimeout(p.ResolvedEndpoint(), p.Kind, key, transport, httpProviderTimeout(p))
		if err != nil {
			return 0, time.Time{}
		}
		identity, err := providers.OllamaModelIdentity(model.Model)
		if err != nil {
			return 0, time.Time{}
		}
		installed, err := adapter.InstalledModels(bounded)
		if err != nil {
			return 0, time.Time{}
		}
		pinned := false
		for _, item := range installed {
			if item.Name == identity && item.Digest == model.ResidencyDigest {
				pinned = true
			}
		}
		if !pinned {
			return 0, time.Time{}
		}
		residents, err := adapter.ResidentModels(bounded)
		now := time.Now().UTC()
		if err != nil || bounded.Err() != nil || len(residents) != 1 {
			return 0, time.Time{}
		}
		r := residents[0]
		name, e1 := providers.OllamaModelIdentity(r.Name)
		actual, e2 := providers.OllamaModelIdentity(r.Model)
		// Dedicated deployment must use indefinite keep-alive; an ordinary
		// short-lived cache is not lifetime authority for warm admission.
		if e1 != nil || e2 != nil || name != identity || actual != identity || r.Digest != model.ResidencyDigest || r.ContextLength != tokens || r.Size > model.RAMBytes || !r.ExpiresAt.After(now.Add(24*time.Hour)) {
			return 0, time.Time{}
		}
		return model.WarmRAMBytes, now
	}
	return 0, time.Time{}
}
