// Package gridroute binds unified automatic selection to trusted remote clients.
package gridroute

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/routing"
)

type Bridge struct {
	executor     executionClient
	mu           sync.Mutex
	cache        map[string]catalogSnapshot
	Client       *remote.Client
	Store        *remote.RouteStore
	EvidenceRoot string
}

func Install(s *app.Service, cfg config.Settings) error {
	c := cfg.WebUI.RemoteClient
	if c == nil || cfg.WebUI.RemoteTrustFile == "" || cfg.WebUI.RemoteDispatchDirectory == "" || cfg.WebUI.RemoteAutomaticEvidenceDirectory == "" {
		return nil
	}
	store, err := remote.OpenRouteStore(cfg.WebUI.RemoteDispatchDirectory)
	if err != nil {
		return err
	}
	client := &remote.Client{ModelAllowed: func(host, model string) bool { return config.ModelUseAllowed(cfg.Telemetry.Database, host, model) }, Trust: remote.TrustFile(cfg.WebUI.RemoteTrustFile), UsageFile: cfg.WebUI.RemoteTrustFile + ".usage.db", Credentials: remote.Credentials{CertificateFile: c.CertificateFile, KeyFile: c.KeyFile, CAFile: c.CAFile}}
	s.ConfigureFederatedRouting(&Bridge{Client: client, Store: store, EvidenceRoot: cfg.WebUI.RemoteAutomaticEvidenceDirectory})
	return nil
}

func (b *Bridge) Candidates(ctx context.Context, r routing.Request) ([]app.FederatedCandidate, error) {
	req := harness.Request{Version: 1, Task: harness.TaskClass{Domain: r.Domain, Profile: r.Profile, Difficulty: "unknown"}, Mode: r.Mode, LocalRequired: r.LocalRequired || r.Mode == "local_only", Capabilities: slices.Clone(r.Capabilities), ContextTokens: int64(max(r.ContextTokens, 8192)), MaxCost: r.MaxCost}
	discovered, infos, err := b.catalog(ctx, req)
	if err != nil {
		return nil, err
	}
	out := []app.FederatedCandidate{}
	for _, proposal := range discovered.Candidates {
		registry, err := b.Client.Trust.Read()
		if err != nil {
			return nil, err
		}
		permitted := false
		for _, peer := range registry.Peers {
			if peer.ID == proposal.Destination && slices.Contains(peer.Operations, "inspect") && slices.Contains(peer.Operations, "cancel") {
				permitted = true
			}
		}
		if !permitted || b.Client.ModelAllowed != nil && !b.Client.ModelAllowed(proposal.Destination, proposal.ModelID) {
			continue
		}
		c := proposal.Candidate
		if !c.Available || !c.Authorized || !c.Compatible || !c.CapacityAvailable || !c.CredentialAvailable {
			continue
		}
		info, ok := infos[proposal.Destination]
		if !ok {
			info, err = b.Client.Info(ctx, proposal.Destination)
			if err != nil {
				continue
			}
			infos[proposal.Destination] = info
		}
		if !info.Available || info.ConversationVersion != 1 {
			continue
		}
		safe := false
		for _, h := range info.Harnesses {
			if h.ID == proposal.HarnessID && h.ModelID == proposal.ModelID && !h.NativeTools {
				safe = true
			}
		}
		if !safe {
			continue
		}
		encoded, _ := json.Marshal(struct {
			Destination, ModelID, HarnessID, Caller string
			Identity                                harness.Identity
		}{proposal.Destination, proposal.ModelID, proposal.HarnessID, discovered.CallerFingerprint, c.Identity})
		digest := sha256.Sum256(encoded)
		id := fmt.Sprintf("paired-%x", digest[:20])
		key := routing.Key{Model: c.Identity.Model, Provider: id, Domain: r.Domain, Profile: r.Profile}
		observations, err := b.Client.RecordedRoutingObservations(ctx, b.EvidenceRoot, discovered.CallerFingerprint, proposal.Destination, c.Identity, req.Task, key)
		if err != nil {
			return nil, err
		}
		locality := "cloud"
		if c.Local {
			locality = "local"
		}
		cost := c.EstimatedCost
		model := config.Model{ID: id, Provider: id, Model: c.Identity.Model, Locality: locality, ContextTokens: int(c.ContextTokens), EstimatedCost: &cost, Capabilities: slices.Clone(c.Capabilities), FailureDomain: proposal.Destination}
		candidate := routing.Candidate{Model: model.Model, Provider: id, Local: c.Local, Capabilities: model.Capabilities, ContextTokens: model.ContextTokens, Healthy: true, PolicyAllowed: true, CapacityAvailable: true, EstimatedCost: cost, FailureDomain: proposal.Destination}
		out = append(out, app.FederatedCandidate{CallerFingerprint: discovered.CallerFingerprint, Identity: c.Identity, Instance: proposal.Destination, Hostname: info.Hostname, DestinationModel: proposal.ModelID, HarnessID: proposal.HarnessID, Model: model, Candidate: candidate, Observations: observations})
		if len(out) > 256 {
			return nil, remote.ErrInvalid
		}
	}
	return out, nil
}
