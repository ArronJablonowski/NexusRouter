package skills

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestActivationOperationRetryAfterRestartAndRollback(t *testing.T) {
	s, path, key, a, b, expected := activationRevisionFixture(t)
	ctx := context.Background()
	var validations atomic.Int32
	validator := ValidatorFunc(func(ctx context.Context, v Version) (Evidence, error) {
		validations.Add(1)
		return pass.Validate(ctx, v)
	})
	if err := s.ActivateOnce(ctx, "activation-operation", expected, b, validator, false); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.ActivationOperation(ctx, key, "activation-operation")
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.ActivationState(ctx, key)
	if err != nil || active.Active != b {
		t.Fatal(active, err)
	}
	committed := activationCatalogBytes(t, path)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if err := s.ActivateOnce(ctx, "activation-operation", expected, b, validator, false); err != nil {
		t.Fatal("lost acknowledgement not recognized", err)
	}
	assertActivationCatalogUnchanged(t, path, committed)
	if validations.Load() != 1 {
		t.Fatal("retry reran validator", validations.Load())
	}
	if err := s.RollbackAt(ctx, active, false); err != nil {
		t.Fatal(err)
	}
	rolled := activationCatalogBytes(t, path)
	if err := s.ActivateOnce(ctx, "activation-operation", expected, b, validator, false); err != nil {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, rolled)
	expectActiveVersion(t, s, key, a)
	repeated, err := s.ActivationOperation(ctx, key, "activation-operation")
	if err != nil || !reflect.DeepEqual(receipt, repeated) || validations.Load() != 1 {
		t.Fatal("receipt changed after rollback", err)
	}
	// The old API retains CAS semantics; operation replay does not weaken it.
	if err := s.ActivateAt(ctx, expected, b, pass, false); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy CAS changed", err)
	}
}

func TestActivationOperationConflictingBindingRejected(t *testing.T) {
	for _, mode := range []string{"target", "revision", "key"} {
		t.Run(mode, func(t *testing.T) {
			s, path, _, a, b, expected := activationRevisionFixture(t)
			ctx := context.Background()
			if err := s.ActivateOnce(ctx, "global-operation", expected, b, pass, false); err != nil {
				t.Fatal(err)
			}
			target := b
			switch mode {
			case "target":
				target = a
			case "revision":
				var err error
				expected, err = s.ActivationState(ctx, expected.Key)
				if err != nil {
					t.Fatal(err)
				}
			case "key":
				draft := sample()
				draft.Key.Name = "different-skill"
				v, err := s.Draft(ctx, draft, false)
				if err != nil {
					t.Fatal(err)
				}
				expected, err = s.ActivationState(ctx, draft.Key)
				if err != nil {
					t.Fatal(err)
				}
				target = v.ID
			}
			before := activationCatalogBytes(t, path)
			validator := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				t.Fatal("conflicting operation invoked validator")
				return Evidence{}, nil
			})
			if err := s.ActivateOnce(ctx, "global-operation", expected, target, validator, false); err == nil {
				t.Fatal("operation binding reused")
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestActivationOperationFailedValidationLeavesNoReceipt(t *testing.T) {
	for _, mode := range []string{"failed", "nondeterministic", "error", "panic", "cancel", "nil"} {
		t.Run(mode, func(t *testing.T) {
			s, path, key, a, b, expected := activationRevisionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var validator Validator = ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				switch mode {
				case "failed":
					return Evidence{ID: "validation", Passed: false, Deterministic: true}, nil
				case "nondeterministic":
					return Evidence{ID: "validation", Passed: true}, nil
				case "error":
					return Evidence{}, errors.New("private validator payload")
				case "panic":
					panic("private validator payload")
				case "cancel":
					cancel()
					return Evidence{ID: "validation", Passed: true, Deterministic: true}, nil
				}
				return Evidence{}, nil
			})
			if mode == "nil" {
				validator = nil
			}
			before := activationCatalogBytes(t, path)
			if err := s.ActivateOnce(ctx, "failed-operation", expected, b, validator, false); err == nil {
				t.Fatal("invalid validation accepted")
			}
			assertActivationCatalogUnchanged(t, path, before)
			if _, err := s.ActivationOperation(context.Background(), key, "failed-operation"); !errors.Is(err, ErrNotFound) {
				t.Fatal("failed attempt got receipt", err)
			}
			expectActiveVersion(t, s, key, a)
		})
	}
}

func TestActivationOperationConcurrentDuplicatesCommitOnce(t *testing.T) {
	s, path, key, _, b, expected := activationRevisionFixture(t)
	ctx := context.Background()
	var before catalog
	if json.Unmarshal(activationCatalogBytes(t, path), &before) != nil {
		t.Fatal("bad catalog")
	}
	var validations atomic.Int32
	validator := ValidatorFunc(func(ctx context.Context, v Version) (Evidence, error) {
		validations.Add(1)
		return pass.Validate(ctx, v)
	})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		other := openTest(t, path)
		wg.Go(func() { errs <- other.ActivateOnce(ctx, "concurrent-operation", expected, b, validator, false) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("duplicate did not acknowledge one commit", err)
		}
	}
	var after catalog
	if json.Unmarshal(activationCatalogBytes(t, path), &after) != nil {
		t.Fatal("bad committed catalog")
	}
	if len(after.Skills[key.index()].Activations) != len(before.Skills[key.index()].Activations)+1 || validations.Load() < 1 {
		t.Fatal("duplicate activation transition")
	}
	if _, err := s.ActivationOperation(ctx, key, "concurrent-operation"); err != nil {
		t.Fatal("commit missing atomic receipt", err)
	}
	expectActiveVersion(t, s, key, b)
	stable := activationCatalogBytes(t, path)
	if err := s.ActivateOnce(ctx, "concurrent-operation", expected, b, validator, false); err != nil {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, stable)
}

func TestActivationOperationValidationABAHasNoReceipt(t *testing.T) {
	s, path, key, a, b, expected := activationRevisionFixture(t)
	ctx := context.Background()
	other := openTest(t, path)
	var afterABA []byte
	validator := ValidatorFunc(func(ctx context.Context, v Version) (Evidence, error) {
		if err := other.Activate(ctx, key, b, a, pass, false); err != nil {
			t.Fatal(err)
		}
		if err := other.Activate(ctx, key, a, b, pass, false); err != nil {
			t.Fatal(err)
		}
		afterABA = activationCatalogBytes(t, path)
		return pass.Validate(ctx, v)
	})
	if err := s.ActivateOnce(ctx, "aba-operation", expected, b, validator, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale activation committed", err)
	}
	if afterABA == nil {
		t.Fatal("validator did not execute")
	}
	assertActivationCatalogUnchanged(t, path, afterABA)
	if _, err := s.ActivationOperation(ctx, key, "aba-operation"); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale activation got receipt", err)
	}
}
