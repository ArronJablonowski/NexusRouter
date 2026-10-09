package app

import (
	"context"
	"slices"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

type specialistScope struct {
	key, domain, profile string
	capabilities         []string
}

var specialistScopes = []specialistScope{
	{"coding", "code", "default", []string{"code", "coding", "reasoning"}},
	{"ocr", "ocr", "ocr-progressive-v1", []string{"ocr", "vision", "image"}},
	{"cli", "commandline", "benchmark", []string{"tools"}},
	{"general", "general", "default", []string{"chat", "reasoning"}},
	{"research", "research", "benchmark-v1", []string{"chat", "reasoning", "summarize"}},
	{"data_analysis", "data_analysis", "benchmark-v1", []string{"code", "reasoning"}},
	{"reasoning", "reasoning", "benchmark-v1", []string{"chat", "reasoning"}},
	{"workflow", "workflow", "benchmark-v1", []string{"tools"}},
	{"translation", "translation", "benchmark-v1", []string{"chat", "translation", "multilingual"}},
	{"audio", "audio", "benchmark-v1", []string{"audio", "speech", "transcription", "speech_to_text", "text_to_speech", "audio_generation"}},
	{"image_generation", "image_generation", "default", []string{"image_generation"}},
	{"video_generation", "video_generation", "default", []string{"video_generation"}},
	{"writing", "writing", "default", []string{"writing", "chat", "summarize"}},
	{"creative", "creative", "default", []string{"creative", "writing", "chat"}},
}

// browserRankings uses current durable observations and the dispatch scorer.
// Rendering must not consume an exploration draw or reserve hardware.
func (s *Service) browserRankings(ctx context.Context, models []contract.ModelInspection) []contract.SpecialistRankingInspection {
	return s.browserRankingsUnified(ctx, &models, false)
}
func (s *Service) browserRankingsUnified(ctx context.Context, modelPage *[]contract.ModelInspection, federated bool) []contract.SpecialistRankingInspection {
	models := *modelPage
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return nil
	}
	defer release()
	p, decay := configuredRoutingPolicy(s.settings)
	p.Exploration = 0
	out := []contract.SpecialistRankingInspection{}
	for _, scope := range specialistScopes {
		remoteObservations := map[string]routing.ObservationSet{}
		if federated {
			tokens := 8192
			if scope.profile != "default" {
				tokens = 32768
			}
			input := providers.Request{}
			remotes, err := s.federatedCandidates(ctx, s.settings, Request{Domain: scope.domain, Profile: scope.profile, LocalRequired: !s.settings.WebUI.SpecialistsAllowCloud}, input, tokens)
			if err != nil {
				return nil
			}
			for _, remote := range remotes {
				remoteObservations[remote.Model.Provider] = remote.Observations
				exists := false
				for _, m := range models {
					if m.ID == remote.Model.ID {
						exists = true
					}
				}
				if !exists {
					if len(models) >= contract.MaxInspectionModels {
						return nil
					}
					tokens := int64(remote.Model.ContextTokens)
					models = append(models, contract.ModelInspection{ID: remote.Model.ID, Provider: remote.Model.Provider, Model: remote.Model.Model, RemoteInstance: remote.Instance, RemoteModelID: remote.DestinationModel, Hostname: remote.Hostname, Locality: remote.Model.Locality, Configured: true, Enabled: true, Installed: remote.Candidate.Local, Usable: true, Capabilities: remote.Model.Capabilities, ContextTokens: &tokens, EstimatedCost: remote.Model.EstimatedCost, Health: "healthy", FailureDomain: remote.Instance})
				}
			}
			*modelPage = models
		}

		observations := map[routing.Key]routing.ObservationSet{}
		validities := map[routing.Key]routing.Validity{}
		sources := map[routing.Key]routing.Key{}
		candidates := []routing.Candidate{}
		ids := map[routing.Key]string{}
		for _, m := range models {
			if m.RemoteInstance != "" {
				if _, ok := remoteObservations[m.Provider]; !ok {
					continue
				}
			}
			if !m.Configured || !m.Usable || m.ContextSelectionStatus == "blocked" || m.ContextTokens == nil || m.EstimatedCost == nil {
				continue
			}
			matches := false
			for _, capability := range scope.capabilities {
				matches = matches || slices.Contains(m.Capabilities, capability)
			}
			// Evidence cannot grant a missing execution or generation capability.
			if !matches && (scope.profile != "default" || scope.key == "image_generation" || scope.key == "video_generation") {
				continue
			}
			key := routing.Key{Model: m.Model, Provider: m.Provider, Domain: scope.domain, Profile: scope.profile}
			set, err := db.ObservationSet(ctx, key, s.settings.Evaluation.Judge)
			if err != nil {
				return nil
			}
			if remote, ok := remoteObservations[m.Provider]; ok {
				set.Fitness = append(set.Fitness, remote.Fitness...)
				set.Advisory = append(set.Advisory, remote.Advisory...)
			}
			source := key
			if m.RemoteInstance == "" && len(set.Fitness) == 0 && len(set.Advisory) == 0 {
				if domain, profile, ok := s.settings.Routing.EvidenceSource(scope.domain, scope.profile); ok {
					source.Domain, source.Profile = domain, profile
					set, err = db.DirectObservationSet(ctx, source)
					if err != nil {
						return nil
					}
				}
			}
			if scope.profile != "default" && len(set.Fitness) == 0 {
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
			candidates = append(candidates, routing.Candidate{Model: m.Model, Provider: m.Provider, FailureDomain: m.FailureDomain, Local: m.Locality == "local", Capabilities: m.Capabilities, ContextTokens: int(*m.ContextTokens), Healthy: m.Usable, PolicyAllowed: true, CapacityAvailable: m.RemoteInstance != "" || m.Locality != "local" || m.RAMBytes != nil && *m.RAMBytes > 0, EstimatedCost: *m.EstimatedCost})
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
		if scope.profile != "default" {
			contextTokens = 32768
		}
		selection, err := routing.Select(routing.Request{Mode: s.settings.Mode, Domain: scope.domain, Profile: scope.profile, LocalRequired: !s.settings.WebUI.SpecialistsAllowCloud, ContextTokens: contextTokens}, p, candidates, evidence, now, 0)
		row := contract.SpecialistRankingInspection{Key: scope.key, Domain: scope.domain, Profile: scope.profile, RequiresEvidence: scope.profile != "default", Models: []contract.SpecialistRankInspection{}}
		if err == nil {
			seenModels := map[string]bool{}
			for _, rank := range selection.Ranked {
				key := routing.Key{Model: rank.Model, Provider: rank.Provider, Domain: scope.domain, Profile: scope.profile}
				source := sources[key]
				deployment := "local/" + ids[key]
				for _, m := range models {
					if m.ID == ids[key] && m.RemoteInstance != "" {
						alias := m.RemoteModelID
						if alias == "" {
							alias = m.Model
						}
						deployment = m.RemoteInstance + "/" + alias
						break
					}
				}
				if seenModels[deployment] {
					continue
				}
				seenModels[deployment] = true

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
