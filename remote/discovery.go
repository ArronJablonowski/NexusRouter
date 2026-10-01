package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"math"
	"slices"
	"strings"
	"time"
)

type DiscoveryExclusion struct{ Destination, ModelID, HarnessID, Reason string }
type CandidateDiscovery struct {
	Version           int
	CallerFingerprint string
	Candidates        []DestinationCandidate
	Excluded          []DiscoveryExclusion
	CheckedAt         time.Time
}

// DiscoverCandidates enumerates only administrator-paired instances. It does not
// grant trust, infer model quality or dispatch. RankRecordedCandidates rechecks
// current capacity/readiness and applies caller-owned outcome evidence.
func (c *Client) DiscoverCandidates(ctx context.Context, request harness.Request) (CandidateDiscovery, error) {
	var out CandidateDiscovery
	if c == nil || ctx == nil || ctx.Err() != nil || request.ContextTokens < 8192 || request.ContextTokens > 1<<24 {
		return out, ErrInvalid
	}
	if _, err := harness.SelectScoped(request, harness.DefaultPolicy(), nil, time.Now().UTC(), 0); err != nil && !errors.Is(err, harness.ErrNoRoute) {
		return out, err
	}
	cert, _, err := c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 {
		return out, ErrDenied
	}
	out.Version = Version
	out.CallerFingerprint = certificateDigest(cert.Certificate[0])
	registry, err := c.Trust.Read()
	if err != nil {
		return out, err
	}
	original := registry.Digest()
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	peers := slices.Clone(registry.Peers)
	slices.SortFunc(peers, func(a, b Peer) int { return strings.Compare(a.ID, b.ID) })
	exclude := func(p Peer, m, h, reason string) {
		out.Excluded = append(out.Excluded, DiscoveryExclusion{p.ID, m, h, reason})
	}
	total := 0
	for _, p := range peers {
		if !p.permits("info") || !p.permits("dispatch") || request.ContextTokens > int64(p.MaxContextTokens) || request.MaxCost > p.MaxCost || (request.LocalRequired && !p.AllowPrivate) {
			exclude(p, "", "", "paired_policy")
			continue
		}
		info, e := c.Catalogue(bounded, p.ID)
		if e != nil {
			if bounded.Err() != nil {
				return CandidateDiscovery{}, bounded.Err()
			}
			exclude(p, "", "", "catalogue_unavailable")
			continue
		}
		if !info.Available {
			exclude(p, "", "", "instance_unavailable")
			continue
		}
		models := map[string]Model{}
		valid := true
		for _, m := range info.Models {
			if !name(m.ID) || !name(m.Provider) || !name(m.Model) {
				valid = false
				break
			}
			if _, ok := models[m.ID]; ok {
				valid = false
				break
			}
			models[m.ID] = m
		}
		seen := map[string]bool{}
		for _, h := range info.Harnesses {
			if !name(h.ID) || h.ID == "auto" || seen[h.ID] {
				valid = false
				break
			}
			seen[h.ID] = true
		}
		if !valid {
			exclude(p, "", "", "invalid_catalogue")
			continue
		}
		for _, h := range info.Harnesses {
			total++
			if total > 4096 {
				return CandidateDiscovery{}, ErrInvalid
			}
			m, ok := models[h.ModelID]
			if !ok {
				exclude(p, h.ModelID, h.ID, "missing_model")
				continue
			}
			intent := Task{Version: 1, ModelID: m.ID, HarnessID: h.ID, HarnessDifficulty: request.Task.Difficulty, Domain: request.Task.Domain, Profile: request.Task.Profile, ContextTokens: int(request.ContextTokens), MaxCost: request.MaxCost, Private: request.LocalRequired, Prompt: "discovery-only"}
			if !p.permitsTask(intent) || (request.Mode == "local_only" && !m.Local) || (request.Mode == "cloud_only" && m.Local) || (request.LocalRequired && !m.Local) {
				exclude(p, m.ID, h.ID, "paired_policy")
				continue
			}
			if m.ContextTokens < int(request.ContextTokens) || m.EstimatedCost == nil || math.IsNaN(*m.EstimatedCost) || math.IsInf(*m.EstimatedCost, 0) || *m.EstimatedCost < 0 || *m.EstimatedCost > request.MaxCost {
				exclude(p, m.ID, h.ID, "model_budget")
				continue
			}
			ready, e := c.HarnessReadiness(bounded, p.ID, HarnessIdentityRequest{m.ID, h.ID, int(request.ContextTokens)})
			if e != nil {
				if bounded.Err() != nil {
					return CandidateDiscovery{}, bounded.Err()
				}
				exclude(p, m.ID, h.ID, "readiness_unavailable")
				continue
			}
			r := ready.Readiness
			if r.Identity.Provider != m.Provider || r.Identity.Model != m.Model || r.Identity.Harness != h.Kind || r.Identity.ModelRevision != h.ModelRevision || r.Local != m.Local {
				exclude(p, m.ID, h.ID, "configuration_changed")
				continue
			}
			caps := []string{}
			for _, cap := range m.Capabilities {
				if slices.Contains(r.Capabilities, cap) {
					caps = append(caps, cap)
				}
			}
			capacityReady := false
			if r.ExecutableMatched && r.ModelState == "present" && r.Compatible && r.CredentialState != "missing" {
				plan, e := c.HarnessCapacity(bounded, p.ID, HarnessIdentityRequest{m.ID, h.ID, int(request.ContextTokens)})
				if bounded.Err() != nil {
					return CandidateDiscovery{}, bounded.Err()
				}
				capacityReady = e == nil && plan.Identity == r.Identity && plan.Capacity.Action == resources.CapacityAdmit
			}
			candidate := harness.Candidate{Identity: r.Identity, Local: r.Local, Available: r.ExecutableMatched && r.ModelState == "present", Authorized: true, Compatible: r.Compatible, CredentialAvailable: r.CredentialState != "missing", CapacityAvailable: capacityReady, Capabilities: caps, ContextTokens: min(int64(m.ContextTokens), r.ContextTokens), EstimatedCost: max(*m.EstimatedCost, r.EstimatedCost)}
			out.Candidates = append(out.Candidates, DestinationCandidate{p.ID, m.ID, h.ID, candidate})
		}
	}
	if bounded.Err() != nil {
		return CandidateDiscovery{}, bounded.Err()
	}
	cert, _, err = c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 || certificateDigest(cert.Certificate[0]) != out.CallerFingerprint {
		return CandidateDiscovery{}, ErrConflict
	}
	after, err := c.Trust.Read()
	if err != nil || after.Digest() != original {
		return CandidateDiscovery{}, ErrConflict
	}
	out.CheckedAt = time.Now().UTC()
	return out, nil
}
