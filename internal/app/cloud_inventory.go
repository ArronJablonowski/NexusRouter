package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

// Discovery lists catalogs only. It never enables a model or sends an inference.
// Scoped remote projections must not discover models outside their allowlist.
func (s *Service) appendCloudInventory(ctx context.Context, out *contract.ModelInspectionPage) {
	out.CloudDiscoveryStatus = "disabled"
	if s.settings.Mode == "local_only" {
		return
	}
	type result struct {
		names []string
		err   error
	}
	results := make([]result, len(s.settings.Providers))
	jobs := make(chan int, len(results))
	count := 0
	for i, p := range s.settings.Providers {
		cloud := p.Kind == "codex_app_server"
		for _, m := range s.settings.Models {
			if m.Provider == p.ID && m.Locality == "cloud" {
				cloud = true
			}
		}
		if cloud {
			jobs <- i
			count++
		}
	}
	close(jobs)
	if count == 0 {
		out.CloudDiscoveryStatus = "not_configured"
		return
	}
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				p := s.settings.Providers[i]
				query, cancel := context.WithTimeout(ctx, 3*time.Second)
				key := ""
				if s.secret != nil && p.APIKeyEnv != "" {
					key = s.secret(p.APIKeyEnv)
				}
				fetch := func(ctx context.Context) ([]string, error) {
					if p.Kind == "codex_app_server" {
						discover := s.codexHealthModels
						if discover == nil {
							discover = codexbridge.HealthModels
						}
						return discover(ctx, p.Executable)
					}
					if p.APIKeyEnv != "" && key == "" {
						return nil, ErrInspection
					}
					transport, err := policy.NewTransport(false, []string{p.ResolvedEndpoint()}, s.settings.DNSAudit())
					if err != nil {
						return nil, err
					}
					defer transport.CloseIdleConnections()
					adapter, err := providers.Build(ctx, s.providerFactory, providers.Connection{Version: 1, ID: p.ID, Endpoint: p.ResolvedEndpoint(), Kind: p.Kind, Purpose: providers.PurposeDiscovery, Timeout: httpProviderTimeout(p), APIKey: key, Transport: transport})
					if err != nil {
						return nil, err
					}
					return adapter.Models(ctx)
				}
				cacheKey := fmt.Sprintf("cloud-inventory:%x", sha256.Sum256([]byte(p.ID+"\x00"+p.Kind+"\x00"+p.ResolvedEndpoint()+"\x00"+p.Executable+"\x00"+key)))
				if s.discovery != nil {
					results[i].names, results[i].err = s.discovery.models(query, cacheKey, fetch)
				} else {
					results[i].names, results[i].err = fetchModels(query, fetch)
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	out.CloudDiscoveryStatus = "complete"
	seen := map[string]bool{}
	for _, m := range out.Models {
		seen[m.Provider+"\x00"+m.Model] = true
	}
	for i, r := range results {
		if r.err != nil {
			out.CloudDiscoveryStatus = "partial"
			continue
		}
		for _, name := range r.names {
			p := s.settings.Providers[i]
			key := p.ID + "\x00" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			item := contract.ModelInspection{ID: fmt.Sprintf("cloud_%x", sha256.Sum256([]byte(key))), Provider: p.ID, Model: name, Locality: "cloud", Capabilities: []string{}, Health: "unknown", StatusCode: "catalog_only"}
			if item.Validate() != nil || len(out.Models) >= contract.MaxInspectionModels {
				out.CloudDiscoveryStatus = "partial"
				continue
			}
			out.Models = append(out.Models, item)
		}
	}
}
