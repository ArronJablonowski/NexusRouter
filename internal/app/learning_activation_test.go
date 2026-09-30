package app

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func learningPass(counter *atomic.Int32) skills.Validator {
	return skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		counter.Add(1)
		return skills.Evidence{ID: "trusted-learning-check", Passed: true, Deterministic: true}, nil
	})
}

func pinValidatedLearning(t *testing.T, svc *Service, validator skills.Validator) skills.LearningState {
	t.Helper()
	for range 8 {
		state, err := svc.LearningStepWithValidation(context.Background(), "trusted-v1", validator)
		if err != nil {
			t.Fatal(state, err)
		}
		if state.PendingSelectionID != "" {
			return state
		}
	}
	t.Fatal("validated selection never pinned")
	return skills.LearningState{}
}

func learningDraftState(t *testing.T, svc *Service, pinned skills.LearningState) (*skills.FileStore, skills.ActivationState, string) {
	t.Helper()
	store, err := skills.Open(svc.settings.Skills.Root, []string{svc.settings.Skills.Scope})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	key := skills.Key{Scope: pinned.Scope, Name: pinned.PendingBucketID}
	history, err := store.History(context.Background(), key)
	if err != nil || len(history.Versions) != 1 {
		t.Fatal(history, err)
	}
	state, err := store.ActivationState(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return store, state, history.Versions[0].Version
}

func TestLearningActivationDurableIntentThenTrustedValidation(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	var checks atomic.Int32
	validator := learningPass(&checks)
	pinned := pinValidatedLearning(t, svc, validator)
	if calls.Load() != 0 || checks.Load() != 0 {
		t.Fatal("planning dispatched work")
	}
	generated, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator)
	if err != nil || !reflect.DeepEqual(generated, pinned) || calls.Load() != 1 || checks.Load() != 0 {
		t.Fatal(generated, err)
	}
	intent, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator)
	if err != nil || !reflect.DeepEqual(intent, pinned) || checks.Load() != 0 {
		t.Fatal("intent tick invoked validator or advanced cursor", intent, err)
	}
	store, before, candidate := learningDraftState(t, svc, pinned)
	if before.Active != "" {
		t.Fatal("unvalidated publication active")
	}
	fresh, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.profile, fresh.toolExtension = svc.profile, svc.toolExtension
	done, err := fresh.LearningStepWithValidation(ctx, "trusted-v1", validator)
	if err != nil || done.PendingSelectionID != "" || done.BucketAfter != pinned.PendingBucketID || checks.Load() != 1 || calls.Load() != 1 {
		t.Fatal(done, err, checks.Load(), calls.Load())
	}
	after, err := store.ActivationState(ctx, before.Key)
	if err != nil || after.Active != candidate {
		t.Fatal(after, err)
	}
	if _, err := store.ActivationOperation(ctx, before.Key, pinned.PendingSelectionID); err != nil {
		t.Fatal("activation missing operation receipt", err)
	}
	inspected, err := fresh.SkillLearningState(ctx)
	if err != nil || !reflect.DeepEqual(inspected, done) {
		t.Fatal(inspected, err)
	}
}

func TestLearningActivationValidationFailureKeepsPending(t *testing.T) {
	for _, mode := range []string{"failed", "error", "panic", "nondeterministic"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, calls := learningFixture(t)
			ctx := context.Background()
			var checks atomic.Int32
			pass := learningPass(&checks)
			pinned := pinValidatedLearning(t, svc, pass)
			for range 2 {
				if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", pass); err != nil {
					t.Fatal(err)
				}
			}
			store, before, candidate := learningDraftState(t, svc, pinned)
			fail := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				switch mode {
				case "error":
					return skills.Evidence{}, errors.New("private validation failure")
				case "panic":
					panic("private validation failure")
				case "nondeterministic":
					return skills.Evidence{ID: "check", Passed: true}, nil
				default:
					return skills.Evidence{ID: "check", Deterministic: true}, nil
				}
			})
			if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", fail); err == nil {
				t.Fatal("failed validator accepted")
			}
			saved, err := svc.SkillLearningState(ctx)
			if err != nil || !reflect.DeepEqual(saved, pinned) {
				t.Fatal("failure moved cursor", saved, err)
			}
			after, err := store.ActivationState(ctx, before.Key)
			if err != nil || after != before {
				t.Fatal("failed validation activated draft", after, err)
			}
			if _, err := store.ActivationOperation(ctx, before.Key, pinned.PendingSelectionID); !errors.Is(err, skills.ErrNotFound) {
				t.Fatal("failure got receipt", err)
			}
			done, err := svc.LearningStepWithValidation(ctx, "trusted-v1", pass)
			if err != nil || done.PendingSelectionID != "" || checks.Load() != 1 || calls.Load() != 1 {
				t.Fatal(done, err, checks.Load(), calls.Load())
			}
			after, err = store.ActivationState(ctx, before.Key)
			if err != nil || after.Active != candidate {
				t.Fatal(after, err)
			}
		})
	}
}

func TestLearningActivationPolicyBindingAndGates(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	var checks atomic.Int32
	validator := learningPass(&checks)
	first, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"changed_validator", "auto_disabled", "nil", "typed_nil", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			copy, freshErr := NewService(svc.settings, svc.secret)
			if freshErr != nil {
				t.Fatal(freshErr)
			}
			copy.profile, copy.toolExtension = svc.profile, svc.toolExtension
			id := "trusted-v1"
			check := validator
			switch mode {
			case "changed_validator":
				id = "trusted-v2"
			case "auto_disabled":
				copy.settings.Skills.AutoActivate = false
			case "nil":
				check = nil
			case "typed_nil":
				var empty skills.ValidatorFunc
				check = empty
			}
			var err error
			if mode == "ordinary" {
				_, err = copy.LearningStep(ctx)
			} else {
				_, err = copy.LearningStepWithValidation(ctx, id, check)
			}
			if err == nil {
				t.Fatal("policy-bound learner admitted incompatible execution")
			}
			saved, err := svc.SkillLearningState(ctx)
			if err != nil || !reflect.DeepEqual(saved, first) || calls.Load() != 0 || checks.Load() != 0 {
				t.Fatal("rejected call changed learner", saved, err)
			}
		})
	}
}

func TestLearningActivationLostAcknowledgementAfterRollback(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	var checks atomic.Int32
	validator := learningPass(&checks)
	pinned := pinValidatedLearning(t, svc, validator)
	if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator); err != nil {
		t.Fatal(err)
	}
	generated, err := svc.PublishSkillGeneration(ctx, pinned.PendingSelectionID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := skills.Open(svc.settings.Skills.Root, []string{pinned.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Seed a prior trusted active workflow so later rollback has a real target.
	draft := generated.Draft
	draft.Steps = []string{"Prior trusted workflow"}
	prior, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	seed := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "prior-check", Passed: true, Deterministic: true}, nil
	})
	if err := store.Activate(ctx, draft.Key, prior.ID, "", seed, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	intent, err := db.LearningActivationIntent(ctx, pinned.Scope, pinned.Name, pinned.PendingSelectionID)
	if err != nil || intent.Expected.Active != prior.ID || intent.Candidate != generated.ID || intent.LearningRevision != pinned.Revision {
		t.Fatal(intent, err)
	}
	// Commit the catalog operation but intentionally leave learner progress
	// untouched, simulating death after catalog acknowledgement was lost.
	if err := svc.ActivateSkillVersionOnce(ctx, intent.SelectionID, intent.Expected, intent.Candidate, validator); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActivationState(ctx, draft.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackAt(ctx, active, false); err != nil {
		t.Fatal(err)
	}
	rolled, err := store.ActivationState(ctx, draft.Key)
	if err != nil || rolled.Active != prior.ID {
		t.Fatal(rolled, err)
	}
	receipt, err := store.ActivationOperation(ctx, draft.Key, intent.SelectionID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.profile, fresh.toolExtension = svc.profile, svc.toolExtension
	done, err := fresh.LearningStepWithValidation(ctx, "trusted-v1", validator)
	if err != nil || done.PendingSelectionID != "" || checks.Load() != 1 || calls.Load() != 1 {
		t.Fatal("lost acknowledgement reran work", done, err, checks.Load(), calls.Load())
	}
	after, err := store.ActivationState(ctx, draft.Key)
	if err != nil || after != rolled {
		t.Fatal("receipt reconciliation undid rollback", after, err)
	}
	repeated, err := store.ActivationOperation(ctx, draft.Key, intent.SelectionID)
	if err != nil || !reflect.DeepEqual(receipt, repeated) {
		t.Fatal("reconciliation changed receipt", err)
	}
}

func TestLearningActivationConflictDoesNotRefreshIntent(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	var checks atomic.Int32
	validator := learningPass(&checks)
	pinned := pinValidatedLearning(t, svc, validator)
	for range 2 {
		if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator); err != nil {
			t.Fatal(err)
		}
	}
	store, state, candidate := learningDraftState(t, svc, pinned)
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.LearningActivationIntent(ctx, pinned.Scope, pinned.Name, pinned.PendingSelectionID)
	if err != nil {
		t.Fatal(err)
	}
	seed := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "external-check", Passed: true, Deterministic: true}, nil
	})
	if err := store.ActivateAt(ctx, state, candidate, seed, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator); err == nil {
			t.Fatal("independent activation mistaken for operation acknowledgement")
		}
		saved, err := svc.SkillLearningState(ctx)
		if err != nil || !reflect.DeepEqual(saved, pinned) {
			t.Fatal("conflict advanced learner", saved, err)
		}
	}
	after, err := db.LearningActivationIntent(ctx, pinned.Scope, pinned.Name, pinned.PendingSelectionID)
	if err != nil || after != before || checks.Load() != 0 || calls.Load() != 1 {
		t.Fatal("conflict refreshed precondition or ran callback", err)
	}
	if _, err := store.ActivationOperation(ctx, state.Key, pinned.PendingSelectionID); !errors.Is(err, skills.ErrNotFound) {
		t.Fatal("foreign activation got operation receipt", err)
	}
}

func TestLearningActivationBackgroundSupervisor(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	var checks atomic.Int32
	learner, err := StartLearningWithValidation(ctx, svc, "trusted-v1", learningPass(&checks))
	if err != nil {
		t.Fatal(err)
	}
	defer learner.Close()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var completed skills.LearningState
	for {
		state, readErr := svc.SkillLearningState(ctx)
		if readErr == nil && state.PendingSelectionID == "" && state.BucketAfter != "" && checks.Load() == 1 {
			completed = state
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("background learner did not complete validated activation", learner.Health())
		case <-learner.done:
			t.Fatal("background learner exited early", learner.Health())
		}
	}
	if err := learner.Close(); err != nil {
		t.Fatal("supervisor close failed", err)
	}
	select {
	case <-learner.done:
	default:
		t.Fatal("Close did not join supervisor")
	}
	key := skills.Key{Scope: completed.Scope, Name: completed.BucketAfter}
	active, err := svc.SkillActivationState(context.Background(), key)
	if err != nil || active.Active == "" || calls.Load() != 1 || checks.Load() != 1 {
		t.Fatal("background activation missing or repeated", active, err, calls.Load(), checks.Load())
	}
	after, err := svc.SkillLearningState(context.Background())
	if err != nil || !reflect.DeepEqual(after, completed) {
		t.Fatal("joined learner changed cursor", after, err)
	}
}

func TestLearningActivationMissingGenerationNeverRedispatches(t *testing.T) {
	svc, _, calls := learningFixture(t)
	ctx := context.Background()
	var checks atomic.Int32
	validator := learningPass(&checks)
	pinned := pinValidatedLearning(t, svc, validator)
	for range 2 {
		if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator); err != nil {
			t.Fatal(err)
		}
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.LearningActivationIntent(ctx, pinned.Scope, pinned.Name, pinned.PendingSelectionID)
	if err != nil || calls.Load() != 1 || checks.Load() != 0 {
		t.Fatal("missing pre-validation intent", err, calls.Load(), checks.Load())
	}
	// Deliberately corrupt only this synthetic database. Disable FK checks on
	// this one connection so the independent durable intent remains present.
	raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	result, err := raw.Exec(`DELETE FROM skill_generation_attempts WHERE id=?`, pinned.PendingSelectionID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatal("fixture did not remove generation", n, err)
	}
	for range 2 {
		if _, err := svc.LearningStepWithValidation(ctx, "trusted-v1", validator); !errors.Is(err, ErrLearningAttention) {
			t.Fatal("missing generation was treated as undispatched", err)
		}
	}
	after, err := db.LearningActivationIntent(ctx, pinned.Scope, pinned.Name, pinned.PendingSelectionID)
	if err != nil || after != before {
		t.Fatal("missing generation changed intent", err)
	}
	saved, err := svc.SkillLearningState(ctx)
	if err != nil || !reflect.DeepEqual(saved, pinned) || calls.Load() != 1 || checks.Load() != 0 {
		t.Fatal("corruption caused execution or cursor progress", saved, err, calls.Load(), checks.Load())
	}
	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM skill_generation_attempts WHERE id=?`, pinned.PendingSelectionID).Scan(&count); err != nil || count != 0 {
		t.Fatal("corrupt generation row recreated", count, err)
	}
}
