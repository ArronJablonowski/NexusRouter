package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type providerInventory struct {
	models    []providers.InstalledModel
	checkedAt time.Time
	err       error
}

var errModelInventory = errors.New("model inventory unavailable")

// localModelInventory discovers installed local models without changing
// provider residency. Provider failures remain isolated so one unavailable
// runtime cannot hide the rest of the inventory.
func (s *Service) localModelInventory(ctx context.Context) map[string]providerInventory {
	result := map[string]providerInventory{}
	if s == nil || ctx == nil {
		return result
	}
	indices := make([]int, 0, len(s.settings.Providers))
	for index, provider := range s.settings.Providers {
		if provider.Kind == "ollama" {
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
						Timeout: httpProviderTimeout(configured), OllamaThink: configured.OllamaThink, APIKey: key, Transport: transport})
					if err == nil {
						if inventory, ok := adapter.(providers.ModelInventoryProvider); ok {
							entry.models, err = inventory.InstalledModels(query)
						} else {
							var names []string
							names, err = adapter.Models(query)
							entry.models = make([]providers.InstalledModel, 0, len(names))
							for _, name := range names {
								entry.models = append(entry.models, providers.InstalledModel{Name: name})
							}
						}
					}
					transport.CloseIdleConnections()
				}
				cancel()
				entry.checkedAt = time.Now().UTC()
				if err != nil || !validProviderInventory(entry.models) {
					entry.models, entry.err = nil, errModelInventory
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

func validProviderInventory(models []providers.InstalledModel) bool {
	if models == nil || len(models) > 4096 {
		return false
	}
	seen := map[string]bool{}
	for _, model := range models {
		identity, err := providers.OllamaModelIdentity(model.Name)
		if err != nil || identity != model.Name || seen[identity] || len(model.Digest) != 0 && len(model.Digest) != 64 ||
			model.ContextTokens < 0 || model.ContextTokens > providers.MaxOutputTokens ||
			!inventoryMetadata(model.Family) || !inventoryMetadata(model.ParameterSize) || !inventoryMetadata(model.Quantization) ||
			!model.ModifiedAt.IsZero() && (model.ModifiedAt.Location() != time.UTC || model.ModifiedAt.Year() < 1970 || model.ModifiedAt.Year() > 2260) {
			return false
		}
		for _, r := range model.Digest {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		seen[identity] = true
	}
	return true
}

func inventoryMetadata(value string) bool {
	return len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}
