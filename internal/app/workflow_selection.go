package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// PlanWorkflowSelection records host grouping attribution, not proof of shared
// procedures. It reads accepted source snapshots without inference or skill-root
// creation. The single-operator database is trusted; save alone is not authority.
func (s *Service) PlanWorkflowSelection(ctx context.Context, modelID string, key skills.Key, group, algorithm string, taskIDs []string, maxCost float64) (skills.WorkflowSelection, error) {
	if algorithm == skills.ObservedToolsAlgorithm {
		return skills.WorkflowSelection{}, ErrAdmission
	}
	return s.planWorkflowSelection(ctx, modelID, key, group, algorithm, taskIDs, maxCost, nil, nil)
}

func (s *Service) planWorkflowSelection(ctx context.Context, modelID string, key skills.Key, group, algorithm string, taskIDs []string, maxCost float64, binding *skills.WorkflowGroup, observedSecrets []string) (skills.WorkflowSelection, error) {
	bad := func() (skills.WorkflowSelection, error) { return skills.WorkflowSelection{}, ErrAdmission }
	if s == nil || ctx == nil || s.settings.Validate() != nil || !s.settings.Skills.Enabled || !s.settings.Skills.AutoDraft || s.settings.Skills.Root == "" || key.Scope != s.settings.Skills.Scope || !skillGenerationIdentifier.MatchString(key.Name) || !skillGenerationIdentifier.MatchString(group) || !skillGenerationIdentifier.MatchString(algorithm) || len(taskIDs) < 2 || len(taskIDs) > 20 {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ids := append([]string(nil), taskIDs...)
	slices.Sort(ids)
	for i, id := range ids {
		if !skillGenerationIdentifier.MatchString(id) || (i > 0 && ids[i-1] == id) {
			return bad()
		}
	}
	secrets := append(append([]string(nil), observedSecrets...), memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean([]any{key, group, algorithm, modelID, ids}, secrets) || ctx.Err() != nil {
		return bad()
	}
	ro, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	var sources []skills.WorkflowSource
	if binding == nil {
		sources, err = ro.SkillWorkflowSources(ctx, ids)
	} else {
		sources, err = ro.SkillWorkflowGroupSources(ctx, ids, binding.ID)
	}
	ro.Close()
	if err != nil || len(sources) != len(ids) {
		return bad()
	}
	if binding != nil && (binding.Validate() != nil || binding.ID != group || binding.Algorithm != algorithm || !slices.Equal(binding.Sources, workflowSelectionCandidates(sources))) {
		return bad()
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	examples := make([]skills.WorkflowExample, len(sources))
	for i, source := range sources {
		examples[i] = source.Example
		examples[i].Steps = redactSkillStrings(source.Example.Steps, secrets)
	}
	if skills.ValidateWorkflowExamples(key, examples) != nil {
		return bad()
	}
	policy, secrets, err := s.workflowSelectionPolicy(modelID, sources, maxCost, secrets)
	if err != nil {
		return bad()
	}
	selection, err := skills.NewWorkflowSelection(key, group, algorithm, modelID, policy, workflowSelectionCandidates(sources), time.Now().UTC())
	if err != nil || !selectionValueClean(selection, secrets) || (binding != nil && !selectionValueClean(binding, secrets)) || ctx.Err() != nil {
		return bad()
	}
	// Refresh before the only write: rotating credentials never enter metadata.
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean(selection, secrets) || (binding != nil && !selectionValueClean(binding, secrets)) {
		return bad()
	}
	store, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer store.Close()
	saved, err := store.SaveWorkflowSelection(ctx, selection)
	if err != nil || saved.Validate() != nil || saved.ID != selection.ID || !selectionValueClean(saved, secrets) || ctx.Err() != nil {
		return bad()
	}
	return saved, nil
}

// GenerateSkillSelection claims selection.ID as the sole generation attempt.
// Sources are reverified within the same snapshot subsequently used for prompts,
// before provider construction. A started/terminal attempt never redispatches.
func (s *Service) GenerateSkillSelection(ctx context.Context, selectionID string, maxCost float64) (skills.GenerationAttempt, error) {
	bad := func() (skills.GenerationAttempt, error) { return skills.GenerationAttempt{}, ErrAdmission }
	if s == nil || ctx == nil || s.settings.Validate() != nil || !skillGenerationIdentifier.MatchString(selectionID) || ctx.Err() != nil {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]string{selectionID, s.settings.Skills.Scope}, secrets) {
		return bad()
	}
	ro, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	selection, err := ro.WorkflowSelection(ctx, s.settings.Skills.Scope, selectionID)
	ro.Close()
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || selection.Validate() != nil || selection.ID != selectionID || !selectionValueClean(selection, secrets) {
		return bad()
	}
	ids := make([]string, len(selection.Sources))
	for i, source := range selection.Sources {
		ids[i] = source.TaskID
	}
	return s.generateSkillDraft(ctx, selection.ID, selection.ModelID, selection.Key, ids, maxCost, &selection, secrets)
}

func workflowSelectionCandidates(sources []skills.WorkflowSource) []skills.WorkflowCandidate {
	out := make([]skills.WorkflowCandidate, len(sources))
	for i, s := range sources {
		out[i] = skills.WorkflowCandidate{TaskID: s.Example.TaskID, SessionID: s.Example.SessionID, Domain: s.Example.Domain, Privacy: s.Privacy, EvaluationID: s.EvaluationID, EvaluationDigest: s.EvaluationDigest, SourceDigest: s.SourceDigest, SourceSequence: s.SourceSequence}
	}
	slices.SortFunc(out, func(a, b skills.WorkflowCandidate) int { return strings.Compare(a.TaskID, b.TaskID) })
	return out
}

// The conservative fingerprint binds all configured policy (including model,
// provider endpoint, resource/privacy/skill settings), plus explicit maxCost.
// Credential values and process-local engine implementations are never hashed.
func (s *Service) workflowSelectionPolicy(modelID string, sources []skills.WorkflowSource, maxCost float64, secrets []string) (string, []string, error) {
	if maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) {
		return "", secrets, ErrAdmission
	}
	var model config.Model
	for _, m := range s.settings.Models {
		if m.ID == modelID {
			model = m
			break
		}
	}
	if model.ID == "" || model.ContextTokens < 1 || model.EstimatedCost == nil || *model.EstimatedCost > maxCost {
		return "", secrets, ErrAdmission
	}
	localRequired := s.settings.Skills.LocalOnly
	for _, source := range sources {
		if source.Privacy != "cloud_allowed" {
			localRequired = true
		}
	}
	local := model.Locality == "local"
	if (s.settings.Mode == "local_only" && !local) || (s.settings.Mode == "cloud_only" && local) || (localRequired && !local) || (local && model.RAMBytes == 0) {
		return "", secrets, ErrAdmission
	}
	var provider config.Provider
	for _, p := range s.settings.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
	if provider.ID == "" {
		return "", secrets, ErrAdmission
	}
	if provider.APIKeyEnv != "" {
		if s.secret == nil {
			return "", secrets, ErrAdmission
		}
		key := s.secret(provider.APIKeyEnv)
		if key == "" {
			return "", secrets, ErrAdmission
		}
		secrets = append(append([]string(nil), secrets...), key)
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	material := struct {
		Version  int
		Settings config.Settings
		ModelID  string
		MaxCost  float64
	}{1, s.settings, modelID, maxCost}
	if !selectionValueClean(material, secrets) {
		return "", secrets, ErrAdmission
	}
	body, err := json.Marshal(material)
	if err != nil || len(body) > 512<<10 {
		return "", secrets, ErrAdmission
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), secrets, nil
}

func selectionValueClean(value any, secrets []string) bool {
	body, err := json.Marshal(value)
	if err != nil || redact(string(body), secrets) != string(body) {
		return false
	}
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return false
	}
	var clean func(any) bool
	clean = func(value any) bool {
		switch v := value.(type) {
		case string:
			return redact(v, secrets) == v
		case []any:
			for _, child := range v {
				if !clean(child) {
					return false
				}
			}
		case map[string]any:
			for key, child := range v {
				if redact(key, secrets) != key || !clean(child) {
					return false
				}
			}
		}
		return true
	}
	return clean(decoded)
}
