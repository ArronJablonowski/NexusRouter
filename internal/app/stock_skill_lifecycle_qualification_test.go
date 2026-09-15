package app

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestStockObservedToolsActivationReuseRestartAndRollback(t *testing.T) {
	svc, _, generated, tasks, generationCalls := observedToolsValidatorFixture(t)
	svc.settings.Skills.Rollback = true
	svc.settings.Skills.Learning.ValidatorID = ObservedToolsProvenanceValidatorID
	registry, err := BuildConfiguredSkillValidatorRegistry(svc, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := registry.Resolve(ObservedToolsProvenanceValidatorID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := validator.Validate(context.Background(), generated)
	if err != nil || proof != (skills.Evidence{ID: ObservedToolsProvenanceValidatorID, Passed: true, Deterministic: true}) {
		t.Fatal("stock validator rejected generated publication", proof, err)
	}

	store, err := skills.Open(svc.settings.Skills.Root, []string{generated.Draft.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	baseline, err := store.Draft(context.Background(), skills.Draft{
		Key:             generated.Draft.Key,
		Description:     "operator baseline",
		Steps:           []string{"Inspect the input manually"},
		SourceSessions:  []string{"operator"},
		ValidationCases: []string{"operator review"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	baselineValidator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "operator-baseline-v1", Passed: true, Deterministic: true}, nil
	})
	if err = store.Activate(context.Background(), baseline.Draft.Key, baseline.ID, "", baselineValidator, false); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(context.Background(), generated.Draft.Key, generated.ID, baseline.ID, validator, true); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActivationState(context.Background(), generated.Draft.Key)
	if err != nil || active.Active != generated.ID {
		t.Fatal("stock validation did not activate generated version", active, err)
	}

	consumer, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	consumer.profile, consumer.toolExtension = svc.profile, svc.toolExtension
	var reused atomic.Bool
	consumer.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return delegateEstimatorProvider(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
			for _, message := range request.Messages {
				var selected struct {
					Skills []contextSkill `json:"procedural_skills"`
				}
				if message.Role == "user" && json.Unmarshal([]byte(message.Content), &selected) == nil {
					for _, candidate := range selected.Skills {
						if candidate.Key == generated.Draft.Key && candidate.Version == generated.ID {
							reused.Store(true)
						}
					}
				}
			}
			return emit(providers.Chunk{Text: "qualified reuse", Done: true, FinishReason: "stop"})
		}), nil
	})
	if _, err = consumer.Run(context.Background(), Request{ModelID: "a", Prompt: "Inspect with lookup", Domain: "code"}); err != nil || !reused.Load() {
		t.Fatal("active generated skill was not progressively reused", err)
	}

	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	restarted.profile, restarted.toolExtension = svc.profile, svc.toolExtension
	restartRegistry, err := BuildConfiguredSkillValidatorRegistry(restarted, nil)
	if err != nil {
		t.Fatal(err)
	}
	restartValidator, err := restartRegistry.Resolve(ObservedToolsProvenanceValidatorID)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := restarted.DurableSkillRegressionStep(context.Background(), "stock-regression", ObservedToolsProvenanceValidatorID, time.Second, restartValidator)
	if err != nil || monitor.Revision == 0 || generationCalls.Load() != 1 {
		t.Fatal("restart did not preserve idempotent stock state", monitor, err, generationCalls.Load())
	}

	history, err := FeedbackHistory(context.Background(), svc.settings.Telemetry.Database, tasks[0])
	if err != nil || len(history) == 0 {
		t.Fatal(err)
	}
	if err = ReviseFeedback(context.Background(), svc.settings.Telemetry.Database, tasks[0], history[len(history)-1].ID, true); err != nil {
		t.Fatal(err)
	}
	if staleProof, staleErr := restartValidator.Validate(context.Background(), generated); !observedToolsRejected(staleProof, staleErr) {
		t.Fatal("stock validator accepted stale evidence", staleProof, staleErr)
	}
	result, err := restarted.RevalidateSkillVersion(context.Background(), active, restartValidator)
	if err != nil || !result.RolledBack || result.Evidence.ID != ObservedToolsProvenanceValidatorID || result.Evidence.Passed || !result.Evidence.Deterministic {
		t.Fatal("stock regression did not reconcile stale evidence", result, err)
	}
	rolled, err := store.ActivationState(context.Background(), generated.Draft.Key)
	if err != nil || rolled.Active != baseline.ID || rolled.Revision == active.Revision {
		t.Fatal("stock regression did not restore baseline", rolled, err)
	}
	after, err := store.History(context.Background(), generated.Draft.Key)
	if err != nil || len(after.Versions) != 2 || len(after.Activations) != 3 {
		t.Fatal("rollback changed immutable version history", after, err)
	}
	rollback := after.Activations[len(after.Activations)-1]
	if !rollback.Rollback || rollback.From != generated.ID || rollback.To != baseline.ID || rollback.Regression == nil || rollback.Regression.ID != ObservedToolsProvenanceValidatorID || rollback.Regression.Passed || !rollback.Regression.Deterministic {
		t.Fatal("rollback lacks stock deterministic evidence", rollback)
	}

	secondRestart, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := secondRestart.SkillActivationState(context.Background(), generated.Draft.Key); err != nil || got != rolled {
		t.Fatal("second restart lost rollback", got, err)
	}
	retained, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{generated.Draft.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	finalHistory, err := retained.History(context.Background(), generated.Draft.Key)
	if err != nil || !reflect.DeepEqual(finalHistory, after) || generationCalls.Load() != 1 {
		t.Fatal("restart changed history or regenerated work", finalHistory, err, generationCalls.Load())
	}
}
