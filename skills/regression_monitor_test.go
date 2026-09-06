package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var allowRegressionMonitor = RegressionMonitorGuard(func(context.Context, RegressionMonitorState, RegressionMonitorCheck) error { return nil })

func prepareMonitorFixture(t *testing.T, s *FileStore, key Key) (RegressionMonitorState, RegressionMonitorCheck) {
	t.Helper()
	a, p, err := s.PrepareRegressionMonitor(context.Background(), key.Scope, "monitor", "validator", strings.Repeat("a", 64), time.Second, allowRegressionMonitor)
	if err != nil || a.Validate() != nil || p.Validate() != nil {
		t.Fatal(a, p, err)
	}
	return a, p
}

func TestRegressionMonitorRestartCadenceAndPolicy(t *testing.T) {
	s, path, key, _, _, expected := regressionFixture(t)
	state, p := prepareMonitorFixture(t, s, key)
	if p.Expected != expected || p.Sequence != state.Revision || p.PreviousCursor != "" || state.PendingOperationID != p.OperationID {
		t.Fatal(state, p)
	}
	other, err := Open(path, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetAutomatic(true)
	got, again := prepareMonitorFixture(t, other, key)
	if got != state || again != p {
		t.Fatal("pending changed across reopen")
	}
	ctx := context.Background()
	if _, err = s.RevalidateAndRollbackOnce(ctx, p.OperationID, p.ValidatorID, p.Expected, pass); !errors.Is(err, ErrConflict) {
		t.Fatal("standalone bypass", err)
	}
	current, err := other.ExecuteRegressionMonitorCheck(ctx, p, pass, allowRegressionMonitor)
	if err != nil || current.After != key.Name || current.PendingOperationID != "" || current.Revision != state.Revision+1 {
		t.Fatal(current, err)
	}
	check, err := s.RegressionMonitorCheck(ctx, key.Scope, "monitor", p.OperationID)
	if err != nil || check.Status != "completed" {
		t.Fatal(check, err)
	}
	if _, err = s.RegressionOperation(ctx, key, p.OperationID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	v := ValidatorFunc(func(context.Context, Version) (Evidence, error) { calls++; return failedRegressionProof(), nil })
	if retry, err := s.ExecuteRegressionMonitorCheck(ctx, p, v, allowRegressionMonitor); err != nil || retry != current || calls != 0 {
		t.Fatal(retry, err, calls)
	}
	noWork, empty, err := s.PrepareRegressionMonitor(ctx, key.Scope, "monitor", "validator", strings.Repeat("a", 64), time.Second, allowRegressionMonitor)
	if err != nil || noWork != current || empty != (RegressionMonitorCheck{}) {
		t.Fatal(noWork, empty, err)
	}
	for _, mode := range []string{"policy", "validator", "interval"} {
		policy, id, interval := strings.Repeat("a", 64), "validator", time.Second
		switch mode {
		case "policy":
			policy = strings.Repeat("b", 64)
		case "validator":
			id = "different"
		case "interval":
			interval = 2 * time.Second
		}
		if _, _, err := s.PrepareRegressionMonitor(ctx, key.Scope, "monitor", id, policy, interval, allowRegressionMonitor); !errors.Is(err, ErrConflict) {
			t.Fatal(mode, err)
		}
	}
	if err = s.with(ctx, func(c *catalog) error {
		a := c.RegressionMonitors[(Key{key.Scope, "monitor"}).index()]
		a.NextDue = time.Now().UTC().Add(-time.Second)
		c.RegressionMonitors[(Key{key.Scope, "monitor"}).index()] = a
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	wrapped, empty, err := s.PrepareRegressionMonitor(ctx, key.Scope, "monitor", "validator", strings.Repeat("a", 64), time.Second, allowRegressionMonitor)
	if err != nil || wrapped.After != "" || empty != (RegressionMonitorCheck{}) {
		t.Fatal(wrapped, empty, err)
	}
}

func TestRegressionMonitorErrorFairnessAndStaleIntent(t *testing.T) {
	for _, mode := range []string{"invalid-validator", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, _, key, _, _, _ := regressionFixture(t)
			_, p := prepareMonitorFixture(t, s, key)
			ctx := context.Background()
			calls := 0
			v := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				calls++
				return Evidence{}, errors.New("private callback error")
			})
			want := "check_failed"
			if mode == "stale" {
				if err := s.RollbackAt(ctx, p.Expected, false); err != nil {
					t.Fatal(err)
				}
				want = "stale_activation"
			}
			state, err := s.ExecuteRegressionMonitorCheck(ctx, p, v, allowRegressionMonitor)
			if err != nil || state.After != p.Expected.Key.Name || state.PendingOperationID != "" {
				t.Fatal(state, err)
			}
			check, err := s.RegressionMonitorCheck(ctx, key.Scope, "monitor", p.OperationID)
			if err != nil || check.Status != "failed" || check.Code != want || check.Expected != p.Expected || mode == "stale" && calls != 0 {
				t.Fatal(check, err, calls)
			}
			if _, err = s.RegressionOperation(ctx, key, p.OperationID); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			if _, err = s.RevalidateAndRollbackOnce(ctx, p.OperationID, p.ValidatorID, p.Expected, pass); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestRegressionMonitorConcurrentOutcomeFences(t *testing.T) {
	for _, receiptFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "receipt-first", false: "failure-first"}[receiptFirst], func(t *testing.T) {
			s, _, key, predecessor, active, expected := regressionFixture(t)
			_, p := prepareMonitorFixture(t, s, key)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{}, 2)
			releaseGood, releaseBad := make(chan struct{}), make(chan struct{})
			good := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				entered <- struct{}{}
				<-releaseGood
				return failedRegressionProof(), nil
			})
			bad := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				entered <- struct{}{}
				<-releaseBad
				return Evidence{}, errors.New("invalid")
			})
			type result struct {
				s RegressionMonitorState
				e error
			}
			goodResult, badResult := make(chan result, 1), make(chan result, 1)
			go func() {
				state, err := s.ExecuteRegressionMonitorCheck(ctx, p, good, allowRegressionMonitor)
				goodResult <- result{state, err}
			}()
			go func() {
				state, err := s.ExecuteRegressionMonitorCheck(ctx, p, bad, allowRegressionMonitor)
				badResult <- result{state, err}
			}()
			<-entered
			<-entered
			var a, b result
			if receiptFirst {
				close(releaseGood)
				a = <-goodResult
				close(releaseBad)
				b = <-badResult
			} else {
				close(releaseBad)
				a = <-badResult
				close(releaseGood)
				b = <-goodResult
			}
			if a.e != nil || b.e != nil || a.s != b.s {
				t.Fatal(a, b)
			}
			check, err := s.RegressionMonitorCheck(ctx, key.Scope, "monitor", p.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.ActivationState(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if receiptFirst {
				if check.Status != "completed" || current.Active != predecessor {
					t.Fatal(check, current)
				}
			} else {
				if check.Status != "failed" || current.Active != active || current != expected {
					t.Fatal("late rollback escaped tombstone", check, current)
				}
			}
		})
	}
}

func TestRegressionMonitorGuardsCancellationAndReceiptReconciliation(t *testing.T) {
	s, path, key, _, _, _ := regressionFixture(t)
	ctx := context.Background()
	before := activationCatalogBytes(t, path)
	deny := RegressionMonitorGuard(func(context.Context, RegressionMonitorState, RegressionMonitorCheck) error {
		return errors.New("private metadata")
	})
	if _, _, err := s.PrepareRegressionMonitor(ctx, key.Scope, "monitor", "validator", strings.Repeat("a", 64), time.Second, deny); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	state, p := prepareMonitorFixture(t, s, key)
	before = activationCatalogBytes(t, path)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ExecuteRegressionMonitorCheck(cancelled, p, pass, allowRegressionMonitor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	completionDeny := RegressionMonitorGuard(func(_ context.Context, _ RegressionMonitorState, p RegressionMonitorCheck) error {
		if p.Status == "completed" {
			return errors.New("rotation")
		}
		return nil
	})
	if _, err := s.ExecuteRegressionMonitorCheck(ctx, p, pass, completionDeny); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	still, err := s.RegressionMonitorState(ctx, key.Scope, "monitor")
	if err != nil || still != state {
		t.Fatal(still, err)
	}
	receipt, err := s.RegressionOperation(ctx, key, p.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err = s.ExecuteRegressionMonitorCheck(ctx, p, ValidatorFunc(func(context.Context, Version) (Evidence, error) { calls++; return Evidence{}, nil }), allowRegressionMonitor); err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	reopened, err := s.RegressionOperation(ctx, key, p.OperationID)
	if err != nil || !reflect.DeepEqual(reopened, receipt) {
		t.Fatal(reopened, err)
	}
}

func TestRegressionMonitorConcurrentPrepareAndGuardPanic(t *testing.T) {
	s, path, key, _, _, _ := regressionFixture(t)
	ctx := context.Background()
	before := activationCatalogBytes(t, path)
	panicGuard := RegressionMonitorGuard(func(context.Context, RegressionMonitorState, RegressionMonitorCheck) error { panic("private panic") })
	if _, _, err := s.PrepareRegressionMonitor(ctx, key.Scope, "monitor", "validator", strings.Repeat("a", 64), time.Second, panicGuard); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	type result struct {
		state RegressionMonitorState
		check RegressionMonitorCheck
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			state, p, err := s.PrepareRegressionMonitor(ctx, key.Scope, "monitor", "validator", strings.Repeat("a", 64), time.Second, allowRegressionMonitor)
			results <- result{state, p, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.state != b.state || a.check != b.check {
		t.Fatal(a, b)
	}
	before = activationCatalogBytes(t, path)
	cancelCtx, cancel := context.WithCancel(ctx)
	v := ValidatorFunc(func(context.Context, Version) (Evidence, error) { cancel(); return failedRegressionProof(), nil })
	if _, err := s.ExecuteRegressionMonitorCheck(cancelCtx, a.check, v, allowRegressionMonitor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}
