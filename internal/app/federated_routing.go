package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
)

// FederatedCandidate is trusted host wiring, never a browser-supplied score.
// Provider and model IDs must bind the destination and exact execution identity.
type FederatedCandidate struct {
	CallerFingerprint                               string
	Identity                                        harness.Identity
	Instance, Hostname, DestinationModel, HarnessID string
	Model                                           config.Model
	Candidate                                       routing.Candidate
	Observations                                    routing.ObservationSet
}

type FederatedRouting interface {
	Candidates(context.Context, routing.Request) ([]FederatedCandidate, error)
	Open(context.Context, FederatedCandidate, string, routing.Request) (providers.Provider, error)
}

type automaticTarget struct {
	ID     string
	Remote *FederatedCandidate
}

// ConfigureFederatedRouting installs the paired execution boundary before serving.
func (s *Service) ConfigureFederatedRouting(f FederatedRouting) { s.federation = f }

func (s *Service) federatedCandidates(ctx context.Context, cfg config.Settings, r Request, input providers.Request, tokens int) ([]FederatedCandidate, error) {
	if s.federation == nil || r.RemoteExecution != nil || r.runtimeHostAdmission != nil || (r.onlyModelID != "" && r.retryTarget == nil) || r.Compaction != nil || r.SummaryAttemptID != "" || r.contextEstimator != nil || len(input.Tools) > 0 || cfg.Tools.Enabled || cfg.Tools.WorkboardReadEnabled || cfg.Tools.WorkboardWriteEnabled || cfg.Workers.DelegateModel != "" || r.delegatedTools != nil {
		return nil, nil
	}
	for _, m := range input.Messages {
		if m.Role == "tool" || len(m.ToolCalls) > 0 || m.ToolCallID != "" || m.ToolFailed {
			return nil, nil
		}
	}
	if tokens < 8192 {
		tokens = 8192
	}
	if r.retryTarget != nil {
		if r.retryTarget.ID != r.onlyModelID {
			return nil, ErrAdmission
		}
		if r.retryTarget.Remote == nil {
			return nil, nil
		}
		return []FederatedCandidate{*r.retryTarget.Remote}, nil
	}
	request := routing.Request{Mode: cfg.Mode, Domain: r.Domain, Profile: r.Profile, LocalRequired: r.LocalRequired, Capabilities: slices.Clone(r.Capabilities), ContextTokens: tokens, MaxCost: r.MaxCost}
	query, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	candidates, err := s.federation.Candidates(query, request)
	if err != nil {
		return nil, errors.Join(ErrAdmission, err)
	}
	if len(candidates) > 256 {
		return nil, ErrAdmission
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		if c.Instance == "" || c.Model.ID == "" || c.Model.Provider == "" || seen[c.Model.Provider] || c.Model.Model != c.Candidate.Model || c.Model.Provider != c.Candidate.Provider || c.Model.ContextTokens != c.Candidate.ContextTokens || c.Candidate.Local != (c.Model.Locality == "local") {
			return nil, ErrAdmission
		}
		seen[c.Model.Provider] = true
	}
	return candidates, nil
}

// Top-three limits distinct model deployments, not multiple harnesses for the
// same destination model. Preserve backend order; never expand the saved pool.
func topModelTargets(selected routing.Selection, remotes map[string]FederatedCandidate, models []config.Model) []automaticTarget {
	targets := []automaticTarget{}
	seen := map[string]bool{}
	for _, rank := range selected.Ranked {
		target := automaticTarget{}
		key := ""
		if remote, ok := remotes[rank.Provider]; ok {
			copy := remote
			target = automaticTarget{ID: remote.Model.ID, Remote: &copy}
			key = remote.Instance + "/" + remote.DestinationModel
		} else {
			for _, m := range models {
				if m.Provider == rank.Provider && m.Model == rank.Model {
					target.ID = m.ID
					key = "local/" + m.ID
					break
				}
			}
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if rank.Provider != selected.Primary.Provider || rank.Model != selected.Primary.Model {
			targets = append(targets, target)
		}
		if len(seen) == 3 {
			break
		}
	}
	return targets
}
