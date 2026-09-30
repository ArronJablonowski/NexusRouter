package app

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestGenerationBudgetStopsNewAttemptBeforeInference(t *testing.T) {
	svc, tasks, calls := selectionAppFixture(t)
	policy := &svc.settings.Skills.GenerationBudget
	policy.Enabled, policy.MaxAttempts, policy.MaxInFlight, policy.Cooldown = true, 1, 1, "0s"
	ctx := context.Background()
	key := skills.Key{Scope: "project", Name: "workflow"}
	first, err := svc.GenerateSkillDraft(ctx, "first", "a", key, tasks, 0)
	if err != nil || first.Status != "drafted" || calls.Load() != 1 {
		t.Fatal(first, err, calls.Load())
	}
	if _, err := svc.GenerateSkillDraft(ctx, "second", "a", skills.Key{Scope: "project", Name: "other"}, tasks, 0); err == nil || calls.Load() != 1 {
		t.Fatal("scope attempt budget bypassed", err, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempts, err := db.ListSkillGenerationAttempts(ctx, "project", "", 20)
	if err != nil || len(attempts) != 1 {
		t.Fatal("denied attempt claimed", attempts, err)
	}
}

func TestGenerationBudgetCountsEarlierUnbudgetedWorkAndCooldown(t *testing.T) {
	svc, tasks, calls := selectionAppFixture(t)
	ctx := context.Background()
	key := skills.Key{Scope: "project", Name: "workflow"}
	if _, err := svc.GenerateSkillDraft(ctx, "unbudgeted", "a", key, tasks, 0); err != nil {
		t.Fatal(err)
	}
	policy := &svc.settings.Skills.GenerationBudget
	policy.Enabled, policy.MaxAttempts, policy.MaxInFlight, policy.Cooldown = true, 10, 1, "1h"
	if _, err := svc.GenerateSkillDraft(ctx, "cooldown", "a", key, tasks, 0); err == nil || calls.Load() != 1 {
		t.Fatal("cooldown ignored earlier work", err, calls.Load())
	}
	if _, err := svc.GenerateSkillDraft(ctx, "different-workflow", "a", skills.Key{Scope: "project", Name: "other"}, tasks, 0); err != nil || calls.Load() != 2 {
		t.Fatal("cooldown leaked across names", err, calls.Load())
	}
}

func TestGenerationBudgetAppliesToSavedSelections(t *testing.T) {
	svc, tasks, calls := selectionAppFixture(t)
	policy := &svc.settings.Skills.GenerationBudget
	policy.Enabled, policy.MaxAttempts, policy.MaxInFlight, policy.Cooldown = true, 1, 1, "0s"
	selection := planAppSelection(t, svc, tasks)
	ctx := context.Background()
	if _, err := svc.GenerateSkillDraft(ctx, "other-attempt", "a", skills.Key{Scope: "project", Name: "other"}, tasks, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || calls.Load() != 1 {
		t.Fatal("selection bypassed scope budget", err, calls.Load())
	}
}

func TestGenerationBudgetRejectsCostAboveAggregateLimit(t *testing.T) {
	svc, tasks, calls := selectionAppFixture(t)
	cost := 0.25
	svc.settings.Models[0].EstimatedCost = &cost
	policy := &svc.settings.Skills.GenerationBudget
	policy.Enabled, policy.MaxCost, policy.MaxAttempts, policy.MaxInFlight, policy.Cooldown = true, 0.4, 10, 1, "0s"
	ctx := context.Background()
	key := skills.Key{Scope: "project", Name: "workflow"}
	if _, err := svc.GenerateSkillDraft(ctx, "first-cost", "a", key, tasks, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GenerateSkillDraft(ctx, "second-cost", "a", key, tasks, 1); err == nil || calls.Load() != 1 {
		t.Fatal("aggregate cost exceeded", err, calls.Load())
	}
}
