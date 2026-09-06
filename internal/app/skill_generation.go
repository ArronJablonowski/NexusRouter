package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

var skillGenerationIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// GenerateSkillDraft derives a durable inactive proposal from accepted task
// snapshots. A stable attempt ID is claimed once before estimation/inference;
// started records after failure are uncertain and never authorize redispatch.
// This does not publish, activate, or mutate the configured SkillStore.
func (s *Service) GenerateSkillDraft(ctx context.Context, attemptID, modelID string, key skills.Key, taskIDs []string, maxCost float64) (skills.GenerationAttempt, error) {
	return s.generateSkillDraft(ctx, attemptID, modelID, key, taskIDs, maxCost, nil, nil)
}

func (s *Service) generateSkillDraft(ctx context.Context, attemptID, modelID string, key skills.Key, taskIDs []string, maxCost float64, selection *skills.WorkflowSelection, observedSecrets []string) (skills.GenerationAttempt, error) {
	bad := func() (skills.GenerationAttempt, error) { return skills.GenerationAttempt{}, ErrAdmission }
	if s == nil || ctx == nil || s.settings.Validate() != nil || s.settings.Telemetry.OTEL || !s.settings.Skills.Enabled || !s.settings.Skills.AutoDraft || s.settings.Skills.Root == "" || key.Scope != s.settings.Skills.Scope || !skillGenerationIdentifier.MatchString(key.Name) || !skillGenerationIdentifier.MatchString(attemptID) || len(taskIDs) < 2 || len(taskIDs) > 20 || maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return bad()
	}
	ids := append([]string(nil), taskIDs...)
	slices.Sort(ids)
	for i, id := range ids {
		if !skillGenerationIdentifier.MatchString(id) || (i > 0 && ids[i-1] == id) {
			return bad()
		}
	}
	secrets := append(append([]string(nil), observedSecrets...), memorySecrets(s.settings, s.secret)...)
	identities := append([]string{attemptID, modelID, key.Scope, key.Name}, ids...)
	for _, id := range identities {
		if redact(id, secrets) != id {
			return bad()
		}
	}
	if s.execution == nil {
		return bad()
	}
	select {
	case s.execution <- struct{}{}:
		defer func() { <-s.execution }()
	case <-ctx.Done():
		return bad()
	}
	read, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	var sources []skills.WorkflowSource
	if selection != nil && selection.Algorithm == skills.ObservedToolsAlgorithm {
		sources, err = read.SkillWorkflowGroupSources(ctx, ids, selection.Group)
	} else {
		sources, err = read.SkillWorkflowSources(ctx, ids)
	}
	read.Close()
	if err != nil || len(sources) != len(ids) {
		return bad()
	}
	if selection != nil {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		fingerprint, observed, err := s.workflowSelectionPolicy(modelID, sources, maxCost, secrets)
		secrets = observed
		if err != nil || selection.Validate() != nil || selection.ID != attemptID || selection.ModelID != modelID || selection.Key != key || selection.PolicyDigest != fingerprint || !slices.Equal(selection.Sources, workflowSelectionCandidates(sources)) || !selectionValueClean(selection, secrets) {
			return bad()
		}
	}
	localRequired := s.settings.Skills.LocalOnly
	rawSteps := make([][]string, len(sources))
	examples := make([]skills.WorkflowExample, 0, len(sources))
	var sourceSessions, sourceEvidence []string
	seenTasks := make(map[string]bool, len(ids))
	for i := range sources {
		source := &sources[i]
		rawSteps[i] = slices.Clone(source.Example.Steps)
		if !slices.Contains(ids, source.Example.TaskID) || seenTasks[source.Example.TaskID] {
			return bad()
		}
		seenTasks[source.Example.TaskID] = true
		if source.Privacy != "cloud_allowed" {
			localRequired = true
		}
		metadata := []string{source.Example.TaskID, source.Example.SessionID, source.Example.Domain, source.Privacy, source.EvaluationID, source.EvaluationDigest, source.SourceDigest}
		for _, check := range source.Example.Checks {
			metadata = append(metadata, check.Reference)
		}
		for _, value := range metadata {
			if redact(value, secrets) != value {
				return bad()
			}
		}
		for j := range source.Example.Steps {
			source.Example.Steps[j] = redact(source.Example.Steps[j], secrets)
		}
		outcome, resolveErr := evaluation.Resolve(source.Example.Checks, false)
		if resolveErr != nil || !outcome.Accepted {
			return bad()
		}
		sourceSessions = append(sourceSessions, source.Example.SessionID)
		sourceEvidence = append(sourceEvidence, outcome.References...)
		examples = append(examples, source.Example)
	}
	if skills.ValidateWorkflowExamples(key, examples) != nil {
		return bad()
	}
	slices.Sort(sourceSessions)
	sourceSessions = slices.Compact(sourceSessions)
	slices.Sort(sourceEvidence)
	sourceEvidence = slices.Compact(sourceEvidence)
	var model config.Model
	for _, m := range s.settings.Models {
		if m.ID == modelID {
			model = m
			break
		}
	}
	if model.ID == "" || model.ContextTokens < 1 || model.EstimatedCost == nil || *model.EstimatedCost > maxCost {
		return bad()
	}
	local := model.Locality == "local"
	if s.settings.Mode == "local_only" && !local || s.settings.Mode == "cloud_only" && local || localRequired && !local {
		return bad()
	}
	var provider config.Provider
	for _, p := range s.settings.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
	if provider.ID == "" || redact(model.Model, secrets) != model.Model || redact(provider.ID, secrets) != provider.ID {
		return bad()
	}
	apiKey := ""
	if s.secret != nil && provider.APIKeyEnv != "" {
		apiKey = s.secret(provider.APIKeyEnv)
	}
	if provider.APIKeyEnv != "" && apiKey == "" {
		return bad()
	}
	if apiKey != "" {
		secrets = append(secrets, apiKey)
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	metadataClean := func() bool {
		return selectionValueClean([]any{identities, workflowSelectionCandidates(sources), sourceSessions, sourceEvidence, model.ID, model.Model, provider.ID, provider.Executable}, secrets) && (selection == nil || selectionValueClean(selection, secrets))
	}
	if !metadataClean() {
		return bad()
	}
	if local {
		if model.RAMBytes == 0 {
			return bad()
		}
		release, err := s.reserveExplicit(ctx, model)
		if err != nil {
			return bad()
		}
		defer release()
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !metadataClean() {
		return bad()
	}
	privacy := "cloud_allowed"
	if localRequired {
		privacy = "local_only"
	}
	adapter, closeProvider, err := s.openAuxiliaryProvider(ctx, provider, model, privacy, apiKey)
	if err != nil {
		return bad()
	}
	defer closeProvider()
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !metadataClean() {
		return bad()
	}
	for i := range sources {
		if provider.Kind == "codex_app_server" {
			// Decode the original host-serialized messages before redaction;
			// literal replacement first can damage JSON or hide escaped secrets.
			clean, err := redactCodexWorkflowSteps(rawSteps[i], secrets)
			if err != nil {
				return bad()
			}
			sources[i].Example.Steps = clean
		} else {
			for j := range sources[i].Example.Steps {
				sources[i].Example.Steps[j] = redact(sources[i].Example.Steps[j], secrets)
			}
		}
		examples[i] = sources[i].Example
	}
	if native, ok := adapter.(*codexAuxiliaryProvider); ok {
		native.beforeStream = func() error {
			secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
			if !metadataClean() {
				return ErrAdmission
			}
			for i := range sources {
				clean, err := redactCodexWorkflowSteps(rawSteps[i], secrets)
				if err != nil || !slices.Equal(clean, sources[i].Example.Steps) {
					return ErrAdmission
				}
			}
			return nil
		}
	}
	body, err := json.Marshal(struct {
		Version                                  int
		Key                                      skills.Key
		Sources                                  []skills.WorkflowSource
		Model                                    config.Model
		ProviderID, ProviderKind, Endpoint, Mode string
		LocalOnly                                bool
		ContextTokens                            int
		Timeout                                  time.Duration
		MaxCost                                  float64
		Executable                               string `json:",omitempty"`
		StructuredOutput                         bool   `json:",omitempty"`
	}{1, key, sources, model, provider.ID, provider.Kind, provider.Endpoint, s.settings.Mode, localRequired, model.ContextTokens, 30 * time.Second, maxCost, provider.Executable, provider.Kind == "codex_app_server"})
	if err != nil || len(body) > 512<<10 {
		return bad()
	}
	digest := sha256.Sum256(body)
	started := skills.GenerationAttempt{Version: 1, ID: attemptID, Key: key, Model: model.Model, Provider: provider.ID, InputDigest: hex.EncodeToString(digest[:]), SourceSessions: sourceSessions, SourceEvidence: sourceEvidence, Status: "started", EstimatedCost: *model.EstimatedCost, StartedAt: time.Now().UTC()}
	if started.Validate() != nil {
		return bad()
	}
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer write.Close()
	if s.settings.Skills.GenerationBudget.Enabled {
		budget, budgetErr := s.skillGenerationBudget()
		if budgetErr != nil {
			return bad()
		}
		err = write.BeginSkillGenerationBudgeted(ctx, started, budget)
	} else {
		err = write.BeginSkillGeneration(ctx, started)
	}
	if err != nil {
		if errors.Is(err, skills.ErrGenerationBudget) {
			return skills.GenerationAttempt{}, skills.ErrGenerationBudget
		}
		return skills.GenerationAttempt{}, skills.ErrGenerationPersistence
	}
	finish := func(terminal skills.GenerationAttempt, cause error) (skills.GenerationAttempt, error) {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if terminal.Validate() != nil || write.FinishSkillGeneration(cleanup, terminal) != nil {
			return started, skills.ErrGenerationPersistence
		}
		return terminal, cause
	}
	fail := func(code string) (skills.GenerationAttempt, error) {
		a := started
		a.Status, a.Code = "failed", code
		a.FinishedAt = time.Now().UTC()
		if a.FinishedAt.Before(a.StartedAt) {
			a.FinishedAt = a.StartedAt
		}
		return finish(a, errors.New("skill generation failed"))
	}
	g := skills.ModelGenerator{Provider: adapter, ContextEstimator: s.contextEstimator, Model: model.Model, ContextTokens: model.ContextTokens, Timeout: 30 * time.Second, EstimatedCost: *model.EstimatedCost, MaxCost: maxCost}
	g.StructuredOutput = provider.Kind == "codex_app_server"
	result, err := g.GenerateDetailed(ctx, key, examples)
	if err != nil {
		code := "generation_failed"
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "canceled"
		}
		return fail(code)
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !metadataClean() {
		return fail("generation_failed")
	}
	draft := result.Draft
	draft.Description = redact(draft.Description, secrets)
	draft.Configuration = redact(draft.Configuration, secrets)
	draft.Tags = redactSkillStrings(draft.Tags, secrets)
	draft.RequiredTools = redactSkillStrings(draft.RequiredTools, secrets)
	draft.Steps = redactSkillStrings(draft.Steps, secrets)
	draft.Risks = redactSkillStrings(draft.Risks, secrets)
	draft.ValidationCases = redactSkillStrings(draft.ValidationCases, secrets)
	draft.SourceSessions = append([]string(nil), sourceSessions...)
	draft.SourceEvidence = append([]string(nil), sourceEvidence...)
	result.Draft = draft
	terminal := started
	terminal.Status, terminal.Result = "drafted", &result
	terminal.FinishedAt = time.Now().UTC()
	if terminal.FinishedAt.Before(terminal.StartedAt) {
		terminal.FinishedAt = terminal.StartedAt
	}
	if terminal.Validate() != nil {
		return fail("generation_failed")
	}
	if ctx.Err() != nil {
		return fail("canceled")
	}
	return finish(terminal, nil)
}
