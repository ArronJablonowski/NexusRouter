package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// Built-in direct routes are local inference with no destination ambient tools.
// The revision identifies configured deployment metadata; an absent weight digest
// is not attestation of immutable model weights.
func (s *Service) directModel(modelID string) (config.Model, config.Provider, error) {
	for _, m := range s.settings.Models {
		if m.ID != modelID {
			continue
		}
		for _, p := range s.settings.Providers {
			if p.ID == m.Provider && (p.Kind == "ollama" || p.Kind == "openai_compatible") && m.Locality == "local" && m.RAMBytes > 0 && m.ContextTokens >= 8192 && m.EstimatedCost != nil {
				return m, p, nil
			}
		}
	}
	return config.Model{}, config.Provider{}, ErrHarnessUnsupported
}
func (s *Service) directIdentity(modelID string, tokens int) (harness.Identity, error) {
	m, p, err := s.directModel(modelID)
	if err != nil || tokens < 8192 || tokens > m.ContextTokens {
		return harness.Identity{}, ErrHarnessUnsupported
	}
	cfgID, err := settingsConfigID(s.settings)
	if err != nil {
		return harness.Identity{}, err
	}
	encoded, err := json.Marshal(struct {
		Model                    config.Model
		ProviderID, Kind, Config string
		Tokens                   int
	}{m, p.ID, p.Kind, cfgID, tokens})
	if err != nil {
		return harness.Identity{}, err
	}
	digest := sha256.Sum256(encoded)
	revision := "config-" + cfgID
	if m.ResidencyDigest != "" {
		revision = "weights-" + m.ResidencyDigest
	}
	return harness.Identity{Version: 1, Harness: "nexus-direct", HarnessVersion: "1", AdapterVersion: "direct-conversation-v1", Provider: m.Provider, Model: m.Model, ModelRevision: revision, ConfigSHA256: fmt.Sprintf("%x", digest)}, nil
}
func (s *Service) directReadiness(ctx context.Context, modelID string, tokens int) (harness.Readiness, error) {
	identity, err := s.directIdentity(modelID, tokens)
	if err != nil {
		return harness.Readiness{}, err
	}
	m, p, err := s.directModel(modelID)
	if err != nil {
		return harness.Readiness{}, err
	}
	out := harness.Readiness{Identity: identity, ExecutableMatched: true, Local: true, Compatible: s.settings.Mode != "cloud_only" && config.ModelUseAllowed(s.settings.Telemetry.Database, "local", m.ID), CredentialState: "not_required", ModelState: "unknown", Capabilities: slices.Clone(m.Capabilities), ContextTokens: int64(m.ContextTokens), EstimatedCost: *m.EstimatedCost}
	if p.APIKeyEnv != "" {
		out.CredentialState = "missing"
		if s.secret != nil && s.secret(p.APIKeyEnv) != "" {
			out.CredentialState = "present"
		}
	}
	if out.Compatible && out.CredentialState != "missing" {
		models, e := s.nativeModelInventory(ctx, p, true)
		if e == nil {
			out.ModelState = "absent"
			if models[m.Model] {
				out.ModelState = "present"
			}
		}
	}
	if ctx.Err() != nil {
		return harness.Readiness{}, ctx.Err()
	}
	return out, out.Validate()
}
func (s *Service) directCapacity(ctx context.Context, modelID string, tokens int) (harness.Identity, resources.Need, resources.CapacityResult, error) {
	identity, err := s.directIdentity(modelID, tokens)
	if err != nil {
		return identity, resources.Need{}, resources.CapacityResult{}, err
	}
	m, _, err := s.directModel(modelID)
	if err != nil {
		return identity, resources.Need{}, resources.CapacityResult{}, err
	}
	sized, err := contextReservationModel(m, tokens)
	if err != nil {
		return identity, resources.Need{}, resources.CapacityResult{}, err
	}
	need := resources.Need{RAM: sized.RAMBytes, VRAM: sized.VRAMBytes, Device: sized.GPUDevice}
	if err := resources.ValidateNeed(need); err != nil {
		return identity, need, resources.CapacityResult{}, err
	}
	if err := s.lockResources(ctx); err != nil {
		return identity, need, resources.CapacityResult{}, err
	}
	defer s.mu.Unlock()
	snapshot, err := s.resourceProfile(ctx)
	if err != nil {
		return identity, need, resources.CapacityResult{}, err
	}
	capacity, err := s.budget.Plan(ctx, resources.CapacityRequest{Version: 1, Snapshot: snapshot, Need: need, Now: s.routingNow()})
	return identity, need, capacity, err
}
