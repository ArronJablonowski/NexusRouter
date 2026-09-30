package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestConfiguredWorkboardDecompositionPolicyReachesRootCardService(t *testing.T) {
	settings := config.Defaults()
	settings.Workboard.Decomposition.MaxDepth = 2
	settings.Workboard.Decomposition.MaxChildrenPerParent = 1
	configID, err := settingsConfigID(settings)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := configuredWorkboardDecompositionPolicy(settings, configID)
	if err != nil || policy.Limits != (workboard.DecompositionLimits{Version: 1, MaxDepth: 2, MaxChildren: 1}) ||
		policy.ConfigDigest != configID || policy.Validate() != nil {
		t.Fatalf("policy=%+v error=%v", policy, err)
	}

	repository := &bridgeBoardRepository{graph: workboard.Graph{BoardID: "board", GraphRevision: 1, LayoutRevision: 1, Nodes: []workboard.Node{}}}
	bridge, err := NewWorkboardBridgeWithDecomposition(repository, repository, time.Now, policy)
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	if err = registerWorkboardMutationTools(registry, bridge); err != nil {
		t.Fatal(err)
	}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: &workboardMutationAuthority{t: t, allowed: true}}
	criterion := map[string]any{"version": 1, "id": "criterion", "kind": "objective", "required_source": "deterministic",
		"validator_id": "validator", "description": "Pass", "required": true}
	out, executeErr := executeWorkboardMutation(t, executor, "configured-policy-call", "workboard_create_card", map[string]any{
		"idempotency_key": "configured-policy-key-1", "board_id": "board", "title": "Card", "criteria": []any{criterion},
		"expected_board_revision": 1, "expected_graph_revision": 1,
	})
	// The fixture deliberately returns no stored card. Reaching that fail-closed
	// response proves the captured mutation passed schema, approval and domain
	// admission without requiring persistence in this application-layer test.
	if !errors.Is(executeErr, tools.ErrExecution) || out.Effect != runtime.UncertainEffect {
		t.Fatalf("out=%+v error=%v", out, executeErr)
	}
	if repository.cardMutation.Decomposition == nil || *repository.cardMutation.Decomposition != policy ||
		repository.cardMutation.Actor.Type != "model" || repository.cardMutation.RequestDigest == "" {
		t.Fatalf("configured policy not bound to root mutation: %+v", repository.cardMutation)
	}
}

func TestConfiguredWorkboardDecompositionPolicyRejectsInvalidOrForgedIdentity(t *testing.T) {
	settings := config.Defaults()
	settings.Workboard.Decomposition.Version = 2
	if _, err := configuredWorkboardDecompositionPolicy(settings, strings.Repeat("a", 64)); err == nil {
		t.Fatal("unsupported decomposition version accepted")
	}
	settings = config.Defaults()
	if _, err := configuredWorkboardDecompositionPolicy(settings, "model-supplied"); err == nil {
		t.Fatal("invalid configuration identity accepted")
	}
	if _, err := NewWorkboardBridgeWithDecomposition(&bridgeBoardRepository{}, &bridgeBoardRepository{}, time.Now, workboard.DecompositionPolicy{}); err == nil {
		t.Fatal("invalid host policy accepted by bridge")
	}
}
