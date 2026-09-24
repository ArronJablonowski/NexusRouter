package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

// A managed endpoint is an explicitly dedicated provider. This state protects
// one Service only, not independent applications sharing that provider.
type residencyEndpoint struct {
	gate      chan struct{}
	active    map[string]int
	uncertain map[string]string
}

func (s *Service) residencyEndpoint(id string) *residencyEndpoint {
	s.residencyMu.Lock()
	defer s.residencyMu.Unlock()
	if s.residencies == nil {
		s.residencies = map[string]*residencyEndpoint{}
	}
	if s.residencies[id] == nil {
		s.residencies[id] = &residencyEndpoint{gate: make(chan struct{}, 1), active: map[string]int{}, uncertain: map[string]string{}}
	}
	return s.residencies[id]
}

func (s *Service) residencyProfile(ctx context.Context) (resources.Snapshot, error) {
	if err := s.lockResources(ctx); err != nil {
		return resources.Snapshot{}, err
	}
	defer s.mu.Unlock()
	return s.resourceProfile(ctx)
}

func (s *Service) reserveManagedResidency(ctx context.Context, provider config.Provider, model config.Model) (func(), error) {
	bad := func() (func(), error) { return nil, errors.Join(ErrAdmission, resources.ErrCapacity) }
	if ctx == nil || ctx.Err() != nil || s.budget == nil || s.providerFactory != nil || provider.Kind != "ollama" || model.Locality != "local" {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := s.residencyEndpoint(provider.ID)
	select {
	case endpoint.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ErrAdmission
	}
	defer func() { <-endpoint.gate }()
	snapshot, err := s.residencyProfile(ctx)
	if err != nil {
		return nil, ErrAdmission
	}
	low, err := s.budget.LowMemory(snapshot, modelResources(model), time.Now())
	if err != nil {
		if errors.Is(err, resources.ErrCapacity) {
			return bad()
		}
		return nil, ErrAdmission
	}
	need := modelResources(model)
	plan, err := s.budget.Plan(ctx, resources.CapacityRequest{Version: resources.CapacityContractVersion, Snapshot: snapshot, Need: need, Now: time.Now()})
	if err != nil {
		return nil, ErrAdmission
	}
	if plan.Action == resources.CapacityWait {
		// Context expansion can exceed available memory even above the fixed
		// 16 GiB low-memory tier. Only a measured RAM/VRAM shortfall permits
		// reclaiming idle residency; pressure and concurrency denials do not.
		shortfall := need.RAM > plan.Headroom.RAMBytes || need.VRAM > 0 && need.VRAM > plan.Headroom.VRAMBytes
		if plan.Reason != resources.CapacityExhausted || !shortfall {
			return bad()
		}
		low = true
	}
	identity, err := providers.OllamaModelIdentity(model.Model)
	if err != nil {
		return nil, ErrAdmission
	}
	if low {
		for other, count := range endpoint.active {
			if count > 0 && other != identity {
				return bad()
			}
		}
	}
	// Outstanding uncertain unloads must be resolved even if headroom improved.
	if low || len(endpoint.uncertain) > 0 {
		key := ""
		if provider.APIKeyEnv != "" && s.secret != nil {
			key = s.secret(provider.APIKeyEnv)
		}
		if provider.APIKeyEnv != "" && key == "" {
			return nil, ErrAdmission
		}
		resolvedEndpoint := provider.ResolvedEndpoint()
		transport, err := policy.NewTransport(true, []string{resolvedEndpoint})
		if err != nil {
			return nil, ErrAdmission
		}
		defer transport.CloseIdleConnections()
		adapter, err := providers.NewHTTPWithTimeout(resolvedEndpoint, provider.Kind, key, transport, httpProviderTimeout(provider))
		if err != nil {
			return nil, ErrAdmission
		}
		if err = s.prepareResidency(ctx, endpoint, adapter, provider, identity, low); err != nil {
			return bad()
		}
	}
	// Always resample after lifecycle requests. No provider-reported size is
	// treated as recovered memory, and no HTTP executes under global s.mu.
	release, err := s.reserveUnmanaged(ctx, model)
	if err != nil {
		return nil, err
	}
	endpoint.active[identity]++
	var once sync.Once
	return func() {
		once.Do(func() {
			endpoint.gate <- struct{}{}
			defer func() { <-endpoint.gate }()
			release()
			endpoint.active[identity]--
			if endpoint.active[identity] == 0 {
				delete(endpoint.active, identity)
			}
		})
	}, nil
}
