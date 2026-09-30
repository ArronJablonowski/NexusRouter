package app

import (
	"context"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// GroupSkillWorkflows groups accepted tasks by actual successful tool execution
// order, domain and profile. Matching names are a bounded drafting heuristic,
// not proof of equivalent semantics, tool implementations or activation quality.
// This reads the single-operator database without inference or catalog writes.
func (s *Service) GroupSkillWorkflows(ctx context.Context, taskIDs []string) ([]skills.WorkflowGroup, error) {
	groups, _, err := s.groupSkillWorkflows(ctx, taskIDs)
	return groups, err
}

func (s *Service) groupSkillWorkflows(ctx context.Context, taskIDs []string) ([]skills.WorkflowGroup, []string, error) {
	bad := func() ([]skills.WorkflowGroup, []string, error) { return nil, nil, ErrAdmission }
	if s == nil || ctx == nil || s.settings.Validate() != nil || !s.settings.Skills.Enabled || !s.settings.Skills.AutoDraft || s.settings.Skills.Root == "" || len(taskIDs) < 1 || len(taskIDs) > 20 {
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
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]any{ids, s.settings.Skills.Scope}, secrets) || ctx.Err() != nil {
		return bad()
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	procedures, err := db.SkillWorkflowProcedures(ctx, ids)
	db.Close()
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || !selectionValueClean([]any{ids, s.settings.Skills.Scope, procedures}, secrets) || ctx.Err() != nil {
		return bad()
	}
	groups := make([]skills.WorkflowGroup, 0)
	if len(procedures) > 0 {
		groups, err = skills.BuildWorkflowGroups(procedures)
	}
	if err != nil || !selectionValueClean(groups, secrets) || ctx.Err() != nil {
		return bad()
	}
	return groups, secrets, nil
}

// PlanGroupedWorkflowSelection derives a reserved grouping rule from durable
// observations. Every requested task must belong to the single returned group;
// singleton, ineligible or repeated-session inputs are never silently dropped.
// The planner compares the group's pinned metadata to its fresh source snapshot
// before saving, so feedback changes between grouping and planning reject.
func (s *Service) PlanGroupedWorkflowSelection(ctx context.Context, modelID string, key skills.Key, taskIDs []string, maxCost float64) (skills.WorkflowSelection, error) {
	if ctx == nil || len(taskIDs) < 2 || len(taskIDs) > 20 {
		return skills.WorkflowSelection{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	groups, secrets, err := s.groupSkillWorkflows(ctx, taskIDs)
	if err != nil || len(groups) != 1 || len(groups[0].Sources) != len(taskIDs) {
		return skills.WorkflowSelection{}, ErrAdmission
	}
	group := groups[0]
	return s.planWorkflowSelection(ctx, modelID, key, group.ID, group.Algorithm, taskIDs, maxCost, &group, secrets)
}
