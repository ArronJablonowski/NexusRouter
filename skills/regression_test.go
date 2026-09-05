package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func regressionFixture(t *testing.T) (*FileStore, string, Key, string, string, ActivationState) {
	t.Helper()
	s, path, key, a, b, state := activationRevisionFixture(t)
	if err := s.ActivateAt(context.Background(), state, b, pass, false); err != nil {
		t.Fatal(err)
	}
	state, err := s.ActivationState(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAutomatic(true)
	return s, path, key, a, b, state
}

func failedRegressionProof() Evidence {
	return Evidence{ID: "deterministic-regression", Passed: false, Deterministic: true}
}

func TestRegressionRevalidationRollbackPersistsEvidenceAcrossRestart(t *testing.T) {
	s, path, key, a, b, state := regressionFixture(t)
	ctx := context.Background()
	calls := 0
	proof := failedRegressionProof()
	result, err := s.RevalidateAndRollback(ctx, state, ValidatorFunc(func(ctx context.Context, v Version) (Evidence, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("unbounded regression validator")
		}
		if v.ID != b || v.Draft.Key != key {
			t.Fatal("wrong version revalidated", v)
		}
		v.Draft.Steps[0] = "mutated-validator-copy"
		return proof, nil
	}))
	if err != nil || !result.RolledBack || !reflect.DeepEqual(result.State, state) || result.Evidence != proof || calls != 1 {
		t.Fatal(result, err, calls)
	}
	expectActiveVersion(t, s, key, a)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	expectActiveVersion(t, s, key, a)
	history, err := s.History(ctx, key)
	if err != nil || history.Active != a || len(history.Versions) != 2 {
		t.Fatal(history, err)
	}
	version, err := s.Load(ctx, key, b)
	if err != nil || version.Draft.Steps[0] == "mutated-validator-copy" {
		t.Fatal("validator mutated immutable skill", version, err)
	}
	events := history.Activations
	if len(events) != 3 || events[2].From != b || events[2].To != a || !events[2].Rollback || events[2].Regression == nil || *events[2].Regression != proof {
		t.Fatal("regression rollback proof lost", events)
	}
	events[2].Regression.ID = "caller-mutated-proof"
	reloaded, err := s.History(ctx, key)
	if err != nil || len(reloaded.Activations) != 3 || reloaded.Activations[2].Regression == nil || *reloaded.Activations[2].Regression != proof {
		t.Fatal("history mutation changed persisted proof", reloaded, err)
	}
}

func TestRegressionPassingEvidenceLeavesActivationUnchanged(t *testing.T) {
	s, path, _, _, _, state := regressionFixture(t)
	before := activationCatalogBytes(t, path)
	proof := Evidence{ID: "still-passing", Passed: true, Deterministic: true}
	r, err := s.RevalidateAndRollback(context.Background(), state, ValidatorFunc(func(context.Context, Version) (Evidence, error) { return proof, nil }))
	if err != nil || r.RolledBack || r.State != state || r.Evidence != proof {
		t.Fatal(r, err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestRegressionInvalidEvidenceAndCancellationCannotMutate(t *testing.T) {
	for _, kind := range []string{"nil", "error", "panic", "nondeterministic", "invalid_id", "canceled_before", "canceled_during"} {
		t.Run(kind, func(t *testing.T) {
			s, path, _, _, _, state := regressionFixture(t)
			before := activationCatalogBytes(t, path)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			validator := Validator(ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				if kind == "canceled_before" {
					t.Error("canceled validator invoked")
				}
				p := failedRegressionProof()
				switch kind {
				case "error":
					return p, errors.New("private-validator-payload")
				case "panic":
					panic("private-validator-payload")
				case "nondeterministic":
					p.Deterministic = false
				case "invalid_id":
					p.ID = "../bad"
				case "canceled_during":
					cancel()
				}
				return p, nil
			}))
			if kind == "nil" {
				validator = nil
			}
			if kind == "canceled_before" {
				cancel()
			}
			r, err := s.RevalidateAndRollback(ctx, state, validator)
			if err == nil || strings.Contains(err.Error(), "private") || r.RolledBack {
				t.Fatal(r, err)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestRegressionRevisionFenceCatchesValidatorABA(t *testing.T) {
	for _, passes := range []bool{false, true} {
		s, path, key, a, b, state := regressionFixture(t)
		other := openTest(t, path)
		var afterABA []byte
		r, err := s.RevalidateAndRollback(context.Background(), state, ValidatorFunc(func(ctx context.Context, _ Version) (Evidence, error) {
			if err := other.Activate(ctx, key, a, b, pass, false); err != nil {
				t.Fatal(err)
			}
			if err := other.Activate(ctx, key, b, a, pass, false); err != nil {
				t.Fatal(err)
			}
			afterABA = activationCatalogBytes(t, path)
			p := failedRegressionProof()
			p.Passed = passes
			return p, nil
		}))
		if !errors.Is(err, ErrConflict) || r.RolledBack {
			t.Fatal("stale regression decision crossed ABA", r, err)
		}
		if afterABA == nil {
			t.Fatal("validator did not run")
		}
		assertActivationCatalogUnchanged(t, path, afterABA)
		expectActiveVersion(t, s, key, b)
	}
}

func TestRegressionKillSwitchReadOnlyAndExhaustedUndo(t *testing.T) {
	s, path, key, _, _, state := regressionFixture(t)
	ctx := context.Background()
	before := activationCatalogBytes(t, path)
	failed := ValidatorFunc(func(context.Context, Version) (Evidence, error) { return failedRegressionProof(), nil })
	s.SetAutomatic(false)
	if _, err := s.RevalidateAndRollback(ctx, state, failed); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	s.SetAutomatic(true)
	if _, err := s.RevalidateAndRollback(ctx, state, ValidatorFunc(func(context.Context, Version) (Evidence, error) {
		s.SetAutomatic(false)
		return failedRegressionProof(), nil
	})); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	ro, err := OpenReadOnly(path, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	ro.SetAutomatic(true)
	if _, err = ro.RevalidateAndRollback(ctx, state, failed); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	s.SetAutomatic(true)
	if _, err = s.RevalidateAndRollback(ctx, state, failed); err != nil {
		t.Fatal(err)
	}
	exhausted, err := s.ActivationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	before = activationCatalogBytes(t, path)
	if r, err := s.RevalidateAndRollback(ctx, exhausted, failed); !errors.Is(err, ErrNotFound) || r.RolledBack {
		t.Fatal(r, err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}
