package skills

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestRegressionOperationPassAndRollbackRetry(t *testing.T) {
	for _, passed := range []bool{true, false} {
		t.Run(map[bool]string{true: "pass", false: "rollback"}[passed], func(t *testing.T) {
			s, path, key, a, b, state := regressionFixture(t)
			ctx := context.Background()
			calls := 0
			validator := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				calls++
				return Evidence{ID: "proof", Passed: passed, Deterministic: true}, nil
			})
			o, err := s.RevalidateAndRollbackOnce(ctx, "operation", "validator", state, validator)
			if err != nil || o.Validate() != nil || o.Expected != state || o.Result.RolledBack == passed || calls != 1 {
				t.Fatal(o, err, calls)
			}
			current, err := s.ActivationState(ctx, key)
			if err != nil || current != o.After || passed && current != state || !passed && current.Active != a {
				t.Fatal(current, err)
			}
			ro, err := OpenReadOnly(path, []string{key.Scope})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ro.RegressionOperation(ctx, key, "operation")
			ro.Close()
			if err != nil || !reflect.DeepEqual(got, o) {
				t.Fatal(got, err)
			}
			if passed {
				err = s.RollbackAt(ctx, current, false)
			} else {
				err = s.ActivateAt(ctx, current, b, pass, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			retry, err := s.RevalidateAndRollbackOnce(ctx, "operation", "validator", state, validator)
			if err != nil || !reflect.DeepEqual(retry, o) || calls != 1 {
				t.Fatal(retry, err, calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
			if _, err = s.RevalidateAndRollbackOnce(ctx, "operation", "different", state, validator); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			s.SetAutomatic(false)
			if _, err = s.RevalidateAndRollbackOnce(ctx, "operation", "validator", state, validator); !errors.Is(err, ErrDisabled) {
				t.Fatal(err)
			}
		})
	}
}

func TestRegressionOperationConcurrentChecksAndStateFence(t *testing.T) {
	s, _, _, _, _, state := regressionFixture(t)
	ctx := context.Background()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	v := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
		entered <- struct{}{}
		<-release
		return failedRegressionProof(), nil
	})
	var wg sync.WaitGroup
	results := make(chan RegressionOperation, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := s.RevalidateAndRollbackOnce(ctx, "same", "validator", state, v)
			results <- o
			errs <- e
		}()
	}
	<-entered
	<-entered
	close(release)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if a, b := <-results, <-results; !reflect.DeepEqual(a, b) {
		t.Fatal(a, b)
	}
	if _, err := s.RevalidateAndRollbackOnce(ctx, "fresh", "validator", state, pass); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestRegressionOperationInvalidValidationNoReceipt(t *testing.T) {
	for name, v := range map[string]Validator{
		"error":            ValidatorFunc(func(context.Context, Version) (Evidence, error) { return Evidence{}, errors.New("private") }),
		"panic":            ValidatorFunc(func(context.Context, Version) (Evidence, error) { panic("private") }),
		"nondeterministic": ValidatorFunc(func(context.Context, Version) (Evidence, error) { return Evidence{ID: "judge", Passed: true}, nil }),
	} {
		t.Run(name, func(t *testing.T) {
			s, path, key, _, _, state := regressionFixture(t)
			before := activationCatalogBytes(t, path)
			if _, err := s.RevalidateAndRollbackOnce(context.Background(), "operation", "validator", state, v); !errors.Is(err, ErrValidation) {
				t.Fatal(err)
			}
			if _, err := s.RegressionOperation(context.Background(), key, "operation"); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestRegressionOperationFencesAndNoPredecessor(t *testing.T) {
	for _, mode := range []string{"kill-switch", "aba", "cancel", "no-predecessor"} {
		t.Run(mode, func(t *testing.T) {
			s, path, key, _, b, state := regressionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "no-predecessor" {
				if err := s.RollbackAt(ctx, state, false); err != nil {
					t.Fatal(err)
				}
				var err error
				state, err = s.ActivationState(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := activationCatalogBytes(t, path)
			v := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				switch mode {
				case "kill-switch":
					s.SetAutomatic(false)
				case "cancel":
					cancel()
				case "aba":
					if err := s.RollbackAt(ctx, state, false); err != nil {
						t.Fatal(err)
					}
					current, err := s.ActivationState(ctx, key)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.ActivateAt(ctx, current, b, pass, false); err != nil {
						t.Fatal(err)
					}
					before = activationCatalogBytes(t, path)
				}
				return failedRegressionProof(), nil
			})
			_, err := s.RevalidateAndRollbackOnce(ctx, "operation", "validator", state, v)
			want := map[string]error{"kill-switch": ErrDisabled, "aba": ErrConflict, "cancel": context.Canceled, "no-predecessor": ErrNotFound}[mode]
			if !errors.Is(err, want) {
				t.Fatal(err, want)
			}
			if _, err := s.RegressionOperation(context.Background(), key, "operation"); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestRegressionOperationCorruptionAndMigration(t *testing.T) {
	for _, mode := range []string{"prefix", "after", "evidence", "time", "schema", "map-key"} {
		t.Run(mode, func(t *testing.T) {
			s, path, key, _, _, state := regressionFixture(t)
			_, err := s.RevalidateAndRollbackOnce(context.Background(), "operation", "validator", state, ValidatorFunc(func(context.Context, Version) (Evidence, error) { return failedRegressionProof(), nil }))
			if err != nil {
				t.Fatal(err)
			}
			var c catalog
			if err = s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			o := c.RegressionOperations["operation"]
			switch mode {
			case "prefix":
				e := c.Skills[key.index()]
				e.Activations[0].At = e.Activations[0].At.Add(1)
				c.Skills[key.index()] = e
			case "after":
				o.After.Revision = state.Revision
			case "evidence":
				o.Result.Evidence.ID = "different"
			case "time":
				o.CheckedAt = o.CheckedAt.Add(1)
			case "schema":
				c.Schema = 3
			case "map-key":
				delete(c.RegressionOperations, "operation")
				c.RegressionOperations["other"] = o
			}
			if mode != "map-key" {
				c.RegressionOperations["operation"] = o
			}
			if err = s.write("catalog.json", c, false); err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			if _, err = s.RegressionOperation(context.Background(), key, "operation"); err == nil {
				t.Fatal("corrupt receipt accepted")
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
	// Activation receipts and new regression receipts coexist without rewriting
	// the activation revision or downgrading schema during later activation.
	s, _, activation := operationRecordFixture(t)
	s.SetAutomatic(true)
	ctx := context.Background()
	state, err := s.ActivationState(ctx, activation.Expected.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevalidateAndRollbackOnce(ctx, activation.OperationID, "validator", state, pass); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActivationOperation(ctx, state.Key, activation.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = s.RollbackAt(ctx, state, false); err != nil {
		t.Fatal(err)
	}
	current, err := s.ActivationState(ctx, state.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateOnce(ctx, "another", current, activation.Candidate, pass, true); err != nil {
		t.Fatal(err)
	}
	var c catalog
	if err = s.read("catalog.json", &c); err != nil || c.Schema != 4 {
		t.Fatal(c.Schema, err)
	}
	if _, err = s.RegressionOperation(ctx, state.Key, activation.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishGeneration(ctx, generationAttemptFixture("drafted"), false); err != nil {
		t.Fatal(err)
	}
	if err = s.read("catalog.json", &c); err != nil || c.Schema != 4 {
		t.Fatal(c.Schema, err)
	}
	if _, err = s.RegressionOperation(ctx, state.Key, activation.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestRegressionOperationCapacityBeforeCallback(t *testing.T) {
	s, path, key, _, _, state := regressionFixture(t)
	ctx := context.Background()
	o, err := s.RevalidateAndRollbackOnce(ctx, "original", "validator", state, pass)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.with(ctx, func(c *catalog) error {
		for i := range 999 {
			copy := o
			copy.OperationID = fmt.Sprintf("check-%d", i)
			c.RegressionOperations[copy.OperationID] = copy
		}
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	calls := 0
	v := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
		calls++
		return Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	if _, err = s.RevalidateAndRollbackOnce(ctx, "new", "validator", state, v); !errors.Is(err, ErrInvalid) || calls != 0 {
		t.Fatal(err, calls)
	}
	if _, err = s.RevalidateAndRollbackOnce(ctx, "original", "validator", state, v); err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	if _, err = s.RegressionOperation(ctx, key, "original"); err != nil {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}
