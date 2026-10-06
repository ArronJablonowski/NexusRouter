package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"io"
	"os"
	"sort"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// runNativeAuto selects only registered native pairs, never a model-only
// substitute. Capacity failures before a durable attempt can eliminate a pair;
// after any task is created, execution never retries on another pair here.
func (s *Service) runNativeAuto(ctx context.Context, r Request) (Result, error) {
	if s.harnessEvidence == nil || validateInput(r) != nil || r.ContextTokens < 8192 || ctx.Err() != nil {
		return Result{}, ErrAdmission
	}
	now := s.routingNow()
	evidence, err := s.harnessEvidence.Snapshot(ctx, now)
	if err != nil {
		return Result{}, err
	}
	digest, err := settingsConfigID(s.settings)
	if err != nil {
		return Result{}, err
	}
	ids := make([]string, 0, len(s.nativeHarnesses))
	for id := range s.nativeHarnesses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var candidates []harness.Candidate
	entries := map[harness.Identity]NativeHarness{}
	artifacts := map[string]bool{}
	inventories := map[string]map[string]bool{}
	toolContract := nativeToolsFor(s.settings, s.toolExtension)
	for _, id := range ids {
		entry := s.nativeHarnesses[id]
		var model config.Model
		var provider config.Provider
		for _, m := range s.settings.Models {
			if m.ID == entry.ModelID {
				model = m
			}
		}
		for _, p := range s.settings.Providers {
			if p.ID == model.Provider {
				provider = p
			}
		}
		c, e := nativeConfig(entry, provider, model, r.ContextTokens, digest, "", deniedNativeTransport{}, nil, toolContract)
		if e != nil {
			return Result{}, e
		}
		identity, e := c.Identity()
		if e != nil {
			return Result{}, ErrAdmission
		}
		if _, exists := entries[identity]; exists {
			return Result{}, ErrAdmission
		}
		entries[identity] = entry
		key := entry.Executable + ":" + entry.ExecutableSHA256
		available, known := artifacts[key]
		if !known {
			available = nativeArtifactMatches(entry)
			artifacts[key] = available
		}
		credential := provider.APIKeyEnv == "" || (s.secret != nil && s.secret(provider.APIKeyEnv) != "")
		cost := 0.0
		if model.EstimatedCost != nil {
			cost = *model.EstimatedCost
		}
		candidate := harness.Candidate{Identity: identity, Local: model.Locality == "local", Available: available, Authorized: true, Compatible: model.EstimatedCost != nil && (len(toolContract.Catalog) == 0 || entry.NativeTools) && (!entry.NativeTools || model.Locality == "local"), CapacityAvailable: model.Locality != "local" || model.RAMBytes > 0, CredentialAvailable: credential, Capabilities: model.Capabilities, ContextTokens: int64(model.ContextTokens), EstimatedCost: cost}

		allowed := credential && available && candidate.Compatible && cost <= r.MaxCost && !(s.settings.Mode == "local_only" && !candidate.Local) && !(s.settings.Mode == "cloud_only" && candidate.Local) && !(r.LocalRequired && !candidate.Local)
		if allowed {
			inventoryKey := provider.ID + ":" + model.Locality
			models, known := inventories[inventoryKey]
			if !known {
				models = s.nativeModels(ctx, provider, candidate.Local)
				inventories[inventoryKey] = models
			}
			candidate.Available = candidate.Available && models[model.Model]
		}
		candidates = append(candidates, candidate)
	}
	selectRoute := s.nativeRouteSelector(r, evidence, now)
	for range candidates {
		selected, e := selectRoute(candidates)
		if e != nil {
			return Result{}, errors.Join(ErrAdmission, e)
		}
		entry := entries[selected.Primary.Identity]
		attempt := r
		attempt.HarnessID = entry.ID
		attempt.ModelID = entry.ModelID
		attempt.nativeHarness = &entry
		attempt.nativeSelection = &selected
		result, e := s.runExplicit(ctx, attempt)
		if e == nil || result.TaskID != "" || !errors.Is(e, resources.ErrCapacity) {
			return result, e
		}
		// A failed capacity reservation has no inference/terminal lineage to replay.
		for i := range candidates {
			if candidates[i].Identity == selected.Primary.Identity {
				candidates[i].CapacityAvailable = false
			}
		}
	}
	return Result{}, errors.Join(ErrAdmission, harness.ErrNoRoute, resources.ErrCapacity)
}

func nativeArtifactMatches(entry NativeHarness) bool {
	file, err := os.Open(entry.Executable)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == entry.ExecutableSHA256
}

// Discover only policy-eligible endpoints; fresh inventory is shared across the
// pairs for this selection, never carried over from an earlier request.
func (s *Service) nativeModels(ctx context.Context, p config.Provider, local bool) map[string]bool {
	models, _ := s.nativeModelInventory(ctx, p, local)
	return models
}
func (s *Service) nativeModelInventory(ctx context.Context, p config.Provider, local bool) (map[string]bool, error) {
	models := map[string]bool{}
	tr, err := policy.NewTransport(s.settings.Mode == "local_only" || local, []string{p.ResolvedEndpoint()}, s.settings.DNSAudit())
	if err != nil {
		return nil, err
	}
	defer tr.CloseIdleConnections()
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	key := ""
	if p.APIKeyEnv != "" && s.secret != nil {
		key = s.secret(p.APIKeyEnv)
	}
	adapter, err := providers.Build(check, s.providerFactory, providers.Connection{Version: 1, ID: p.ID, Endpoint: p.ResolvedEndpoint(), Kind: p.Kind, Purpose: providers.PurposeDiscovery, Timeout: httpProviderTimeout(p), OllamaThink: p.OllamaThink, APIKey: key, Transport: tr})
	if err != nil {
		return nil, err
	}
	names, err := adapter.Models(check)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		models[name] = true
	}
	return models, nil
}

// Take one independent draw per evaluation request, reused after pre-dispatch
// capacity exclusions. Ordinary tasks never consume an exploration draw.
func (s *Service) nativeEvaluationPolicy(r Request) (harness.Policy, float64) {
	p := harness.DefaultPolicy()
	p.Exploration = s.settings.Routing.Exploration
	draw := 0.0
	if r.HarnessEvaluation && p.Exploration > 0 {
		s.mu.Lock()
		draw = s.draw()
		s.mu.Unlock()
	}
	return p, draw
}

func (s *Service) nativeRouteSelector(r Request, evidence *harness.Snapshot, now time.Time) func([]harness.Candidate) (harness.Selection, error) {
	request := harness.Request{Version: 1, AllowExploration: r.HarnessEvaluation, Task: harness.TaskClass{Domain: r.Domain, Profile: r.Profile, Difficulty: nativeDifficulty(r.HarnessDifficulty)}, Mode: s.settings.Mode, LocalRequired: r.LocalRequired, Capabilities: r.Capabilities, ContextTokens: int64(r.ContextTokens), MaxCost: r.MaxCost}
	p, draw := s.nativeEvaluationPolicy(r)
	return func(candidates []harness.Candidate) (harness.Selection, error) {
		return harness.Select(request, p, candidates, evidence, now, draw)
	}
}
