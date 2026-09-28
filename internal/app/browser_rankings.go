package app

import (
	"context"
	"slices"

	"github.com/ArronJablonowski/DarwinRouter/routing"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

type specialistScope struct {
	key, domain, profile string
	capabilities         []string
}

var specialistScopes = []specialistScope{
	{"coding", "code", "default", []string{"code", "coding", "reasoning"}},
	{"ocr", "ocr", "default", []string{"ocr", "vision", "image"}},
	{"cli", "commandline", "benchmark", []string{"tools"}},
	{"general", "general", "default", []string{"chat", "reasoning"}},
	{"image_generation", "image_generation", "default", []string{"image_generation", "image", "vision"}},
	{"video_generation", "video_generation", "default", []string{"video_generation", "video", "vision"}},
	{"writing", "writing", "default", []string{"writing", "chat", "summarize"}},
	{"creative", "creative", "default", []string{"creative", "writing", "chat"}},
}

// browserRankings uses current durable observations and the dispatch scorer.
// Rendering must not consume an exploration draw or reserve hardware.
func (s *Service) browserRankings(ctx context.Context, models []contract.ModelInspection) []contract.SpecialistRankingInspection {
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return nil
	}
	defer release()
	p, decay := configuredRoutingPolicy(s.settings)
	p.Exploration = 0
	out := []contract.SpecialistRankingInspection{}
	for _, scope := range specialistScopes {
		observations := map[routing.Key]routing.ObservationSet{}
		validities := map[routing.Key]routing.Validity{}
		sources := map[routing.Key]routing.Key{}
		candidates := []routing.Candidate{}
		ids := map[routing.Key]string{}
		for _, m := range models {
			if !m.Configured || !m.Usable || m.ContextSelectionStatus == "blocked" || m.ContextTokens == nil || m.EstimatedCost == nil {
				continue
			}
			matches := false
			for _, capability := range scope.capabilities {
				matches = matches || slices.Contains(m.Capabilities, capability)
			}
			key := routing.Key{Model: m.Model, Provider: m.Provider, Domain: scope.domain, Profile: scope.profile}
			set, err := db.ObservationSet(ctx, key, s.settings.Evaluation.Judge)
			if err != nil {
				return nil
			}
			source := key
			if len(set.Fitness) == 0 && len(set.Advisory) == 0 {
				if domain, profile, ok := s.settings.Routing.EvidenceSource(scope.domain, scope.profile); ok {
					source.Domain, source.Profile = domain, profile
					set, err = db.DirectObservationSet(ctx, source)
					if err != nil {
						return nil
					}
				}
			}
			if scope.profile == "benchmark" && (len(set.Fitness) == 0 || !matches) {
				continue
			}
			if !matches && len(set.Fitness) == 0 && len(set.Advisory) == 0 {
				continue
			}
			v, err := db.OutputValidity(ctx, key, "")
			if err != nil {
				return nil
			}
			validities[key] = v
			observations[key], sources[key], ids[key] = set, source, m.ID
			candidates = append(candidates, routing.Candidate{Model: m.Model, Provider: m.Provider, FailureDomain: m.FailureDomain, Local: m.Locality == "local", Capabilities: m.Capabilities, ContextTokens: int(*m.ContextTokens), Healthy: m.Usable, PolicyAllowed: true, CapacityAvailable: m.Locality != "local" || m.RAMBytes != nil && *m.RAMBytes > 0, EstimatedCost: *m.EstimatedCost})
		}
		now := s.routingNow()
		evidence := map[routing.Key]routing.Evidence{}
		for key, set := range observations {
			e, err := routing.AggregateEvidence(sources[key], set, now, p, decay)
			if err != nil {
				return nil
			}
			if source := sources[key]; source != key && e.Samples > 0 {
				e.SourceDomain, e.SourceProfile = source.Domain, source.Profile
			}
			if v := validities[key]; v.Samples > 0 {
				e.Validity = v
				if e.Updated.IsZero() {
					e.Updated = v.Updated
				}
			}
			evidence[key] = e
		}
		contextTokens := 1
		if scope.profile == "benchmark" {
			contextTokens = 32768
		}
		selection, err := routing.Select(routing.Request{Mode: s.settings.Mode, Domain: scope.domain, Profile: scope.profile, LocalRequired: !s.settings.WebUI.SpecialistsAllowCloud, ContextTokens: contextTokens}, p, candidates, evidence, now, 0)
		row := contract.SpecialistRankingInspection{Key: scope.key, Domain: scope.domain, Profile: scope.profile, Models: []contract.SpecialistRankInspection{}}
		if err == nil {
			for _, rank := range selection.Ranked {
				key := routing.Key{Model: rank.Model, Provider: rank.Provider, Domain: scope.domain, Profile: scope.profile}
				source := sources[key]
				row.Models = append(row.Models, contract.SpecialistRankInspection{ModelID: ids[key], Domain: source.Domain, Profile: source.Profile, Score: rank.Score, Confidence: rank.Confidence, Samples: rank.Samples})
				if len(row.Models) == 3 {
					break
				}
			}
		}
		out = append(out, row)
	}
	return out
}
