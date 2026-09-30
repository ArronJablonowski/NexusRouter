package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func (s *Service) prepareResidency(ctx context.Context, state *residencyEndpoint, adapter providers.ResidencyController, provider config.Provider, target string, low bool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(s.settings.Models) > 256 {
		return ErrAdmission
	}
	known := map[string]bool{}
	for _, model := range s.settings.Models {
		if model.Provider == provider.ID {
			if model.Locality != "local" {
				return ErrAdmission
			}
			id, err := providers.OllamaModelIdentity(model.Model)
			if err != nil || known[id] {
				return ErrAdmission
			}
			known[id] = true
		}
	}
	residents, err := adapter.ResidentModels(ctx)
	if err != nil {
		return ErrAdmission
	}
	present := map[string]bool{}
	digests := map[string]bool{}
	for _, resident := range residents {
		id, err := providers.OllamaModelIdentity(resident.Model)
		if err != nil || !known[id] || present[id] || digests[resident.Digest] {
			return ErrAdmission
		}
		present[id] = true
		digests[resident.Digest] = true
	}
	for id, digest := range state.uncertain {
		if present[id] || digests[digest] {
			return ErrAdmission
		}
		delete(state.uncertain, id)
	}
	if !low {
		return nil
	}
	count := 0
	for id := range present {
		if id != target {
			count++
		}
	}
	if count > 8 {
		return ErrAdmission
	}
	for _, resident := range residents {
		id, _ := providers.OllamaModelIdentity(resident.Model)
		if id == target {
			continue
		}
		if state.active[id] > 0 {
			return ErrAdmission
		}
		// Mark before dispatch: failures retain uncertainty across queued retries.
		// Only fresh observed absence or a confirmed unload clears it.
		state.uncertain[id] = resident.Digest
		if adapter.UnloadModel(ctx, resident.Model) != nil {
			return ErrAdmission
		}
		confirmed, err := adapter.ResidentModels(ctx)
		if err != nil {
			return ErrAdmission
		}
		for _, item := range confirmed {
			other, e := providers.OllamaModelIdentity(item.Model)
			if e != nil || !known[other] || other == id || item.Digest == resident.Digest {
				return ErrAdmission
			}
		}
		delete(state.uncertain, id)
	}
	return ctx.Err()
}
