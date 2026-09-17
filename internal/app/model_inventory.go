package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type providerInventory struct {
	models []providers.InstalledModel
	err    error
}

// localModelInventory discovers installed local models without changing
// provider residency. Provider failures remain isolated so one unavailable
// runtime cannot hide the rest of the inventory.
func (s *Service) localModelInventory(ctx context.Context, allowed map[string]bool) map[string]providerInventory {
	result := map[string]providerInventory{}
	if s == nil || ctx == nil {
		return result
	}
	indices := make([]int, 0, len(s.settings.Providers))
	for index, provider := range s.settings.Providers {
		if provider.Kind == "ollama" && allowed[provider.ID] {
			indices = append(indices, index)
		}
	}
	jobs := make(chan int, len(indices))
	for _, index := range indices {
		jobs <- index
	}
	close(jobs)
	var mu sync.Mutex
	var group sync.WaitGroup
	workers := 4
	if len(indices) < workers {
		workers = len(indices)
	}
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				configured := s.settings.Providers[index]
				entry := providerInventory{}
				query, cancel := context.WithTimeout(ctx, 2*time.Second)
				transport, err := policy.NewTransport(true, []string{configured.ResolvedEndpoint()})
				if err == nil {
					key := ""
					if s.secret != nil && configured.APIKeyEnv != "" {
						key = s.secret(configured.APIKeyEnv)
					}
					var adapter providers.Provider
					adapter, err = providers.Build(query, s.providerFactory, providers.Connection{Version: 1, ID: configured.ID,
						Endpoint: configured.ResolvedEndpoint(), Kind: configured.Kind, Purpose: providers.PurposeDiscovery,
						Timeout: httpProviderTimeout(configured), APIKey: key, Transport: transport})
					if err == nil {
						if inventory, ok := adapter.(providers.ModelInventoryProvider); ok {
							entry.models, err = inventory.InstalledModels(query)
						} else {
							var names []string
							names, err = adapter.Models(query)
							for _, name := range names {
								entry.models = append(entry.models, providers.InstalledModel{Name: name})
							}
						}
					}
					transport.CloseIdleConnections()
				}
				cancel()
				if err != nil {
					entry.models, entry.err = nil, err
				} else {
					sort.Slice(entry.models, func(i, j int) bool { return entry.models[i].Name < entry.models[j].Name })
				}
				mu.Lock()
				result[configured.ID] = entry
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	return result
}
