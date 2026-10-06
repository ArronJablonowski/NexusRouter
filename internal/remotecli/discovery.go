package remotecli

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

// Each info request performs fresh, policy-bound inventory discovery without
// inference or residency mutations. Configured capabilities remain configured
// claims: presence in a model catalogue does not attest weights or tools.
func modelObserver(cfg config.Settings, secret func(string) string, profile func(context.Context) (resources.Snapshot, error)) func(context.Context, []remote.Model) ([]remote.ModelObservation, *remote.ResourceObservation, error) {
	return func(ctx context.Context, models []remote.Model) ([]remote.ModelObservation, *remote.ResourceObservation, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		type inventory struct {
			names   map[string]bool
			known   bool
			checked time.Time
		}
		inventories := map[string]inventory{}
		out := make([]remote.ModelObservation, len(models))
		for i, m := range models {
			local := cfg.Mode == "local_only" || m.Local
			key := m.Provider + "/remote"
			if local {
				key += "/local"
			}
			found, ok := inventories[key]
			if !ok {
				found = inventory{checked: time.Now().UTC()}
				var p *config.Provider
				for j := range cfg.Providers {
					if cfg.Providers[j].ID == m.Provider {
						p = &cfg.Providers[j]
						break
					}
				}
				eligible := p != nil && !(cfg.Mode == "cloud_only" && m.Local) && !(cfg.Mode == "local_only" && !m.Local)
				if eligible && ctx.Err() == nil {
					apiKey := ""
					if p.APIKeyEnv != "" && secret != nil {
						apiKey = secret(p.APIKeyEnv)
					}
					if p.APIKeyEnv == "" || apiKey != "" {
						tr, e := policy.NewTransport(local, []string{p.ResolvedEndpoint()}, cfg.DNSAudit())
						if e == nil {
							probe, stop := context.WithTimeout(ctx, 2*time.Second)
							adapter, err := providers.Build(probe, nil, providers.Connection{Version: 1, ID: p.ID, Endpoint: p.ResolvedEndpoint(), Kind: p.Kind, Purpose: providers.PurposeDiscovery, Timeout: 2 * time.Second, APIKey: apiKey, Transport: tr})
							if err == nil {
								names, e := adapter.Models(probe)
								if e == nil && probe.Err() == nil {
									found.known = true
									found.names = map[string]bool{}
									for _, name := range names {
										found.names[name] = true
									}
								}
							}
							stop()
							tr.CloseIdleConnections()
						}
					}
				}
				inventories[key] = found
			}
			state := "unknown"
			if found.known {
				state = "absent"
				if found.names[m.Model] {
					state = "present"
				}
			}
			out[i] = remote.ModelObservation{State: state, CheckedAt: found.checked}
		}
		measured := &remote.ResourceObservation{State: "unknown", CheckedAt: time.Now().UTC()}
		if profile != nil && ctx.Err() == nil {
			probe, stop := context.WithTimeout(ctx, 2*time.Second)
			snapshot, e := profile(probe)
			if e == nil && probe.Err() == nil && !snapshot.Time.IsZero() && !snapshot.Time.After(time.Now()) && time.Since(snapshot.Time) <= 5*time.Second && snapshot.TotalRAM > 0 && snapshot.AvailableRAM <= snapshot.TotalRAM {
				measured.State = "measured"
				measured.CheckedAt = snapshot.Time
				measured.TotalRAM = &snapshot.TotalRAM
				measured.AvailableRAM = &snapshot.AvailableRAM
			}
			stop()
		}
		if ctx.Err() != nil {
			return nil, nil, remote.ErrUnavailable
		}
		return out, measured, nil
	}
}
