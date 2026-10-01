package app

import (
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// NativeHarnessIdentity derives the exact configured identity for a requested
// context without opening providers, reading secrets, admitting or running work.
// It is not an artifact/capability attestation or a resource reservation.
func (s *Service) NativeHarnessIdentity(modelID, registration string, tokens int) (harness.Identity, error) {
	if s == nil {
		return harness.Identity{}, ErrAdmission
	}
	entry, ok := s.nativeHarnesses[registration]
	if !ok || entry.ModelID != modelID {
		return harness.Identity{}, ErrHarnessUnsupported
	}
	var model config.Model
	var provider config.Provider
	for _, m := range s.settings.Models {
		if m.ID == modelID {
			model = m
		}
	}
	if model.ID == "" || tokens < 8192 || tokens > model.ContextTokens {
		return harness.Identity{}, ErrHarnessUnsupported
	}
	for _, p := range s.settings.Providers {
		if p.ID == model.Provider {
			provider = p
		}
	}
	digest, err := settingsConfigID(s.settings)
	if err != nil {
		return harness.Identity{}, err
	}
	c, err := nativeConfig(entry, provider, model, tokens, digest, "", deniedNativeTransport{}, nil, nativeToolsFor(s.settings, s.toolExtension))
	if err != nil {
		return harness.Identity{}, err
	}
	return c.Identity()
}
