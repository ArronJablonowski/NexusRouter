package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSkillRegressionStepRollbackAndWrap(t *testing.T) {
	svc, first, second, state := appRegressionFixture(t)
	ctx := context.Background()
	svc.settings.Skills.AutoActivate = false
	svc.settings.Skills.Learning.Enabled = false
	var checks atomic.Int32
	validator := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
		checks.Add(1)
		return skills.Evidence{ID: "monitor-check", Passed: v.ID != second.ID, Deterministic: true}, nil
	})
	next, err := svc.SkillRegressionStep(ctx, "", validator)
	if err != nil || next != state.Key.Name || checks.Load() != 1 {
		t.Fatal(next, err, checks.Load())
	}
	after, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil || after.Active != first.ID {
		t.Fatal(after, err)
	}
	if wrapped, err := svc.SkillRegressionStep(ctx, next, validator); err != nil || wrapped != "" || checks.Load() != 1 {
		t.Fatal(wrapped, err)
	}
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SkillRegressionStep(ctx, "", validator); err != nil || checks.Load() != 2 {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		t.Fatal("passing repeat rewrote catalog", err)
	}
}

func TestSkillRegressionStepBadEvidenceAdvancesWithoutMutation(t *testing.T) {
	for _, mode := range []string{"error", "panic", "nondeterministic", "invalid_id"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _, state := appRegressionFixture(t)
			ctx := context.Background()
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				switch mode {
				case "error":
					return skills.Evidence{}, errors.New("private callback failure")
				case "panic":
					panic("private callback failure")
				case "invalid_id":
					return skills.Evidence{ID: "bad id", Deterministic: true}, nil
				default:
					return skills.Evidence{ID: "check", Passed: false}, nil
				}
			})
			next, err := svc.SkillRegressionStep(ctx, "", validator)
			if next != state.Key.Name || !errors.Is(err, ErrAdmission) {
				t.Fatal(next, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("bad evidence changed activation", err)
			}
		})
	}
}

func TestSkillRegressionMonitorBackgroundRollback(t *testing.T) {
	svc, first, second, state := appRegressionFixture(t)
	svc.settings.Skills.AutoActivate = false
	svc.settings.Skills.Learning.Enabled = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var checks atomic.Int32
	validator := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
		checks.Add(1)
		return skills.Evidence{ID: "monitor-regression", Passed: v.ID != second.ID, Deterministic: true}, nil
	})
	monitor, err := StartSkillRegression(ctx, svc, time.Second, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err := svc.SkillActivationState(ctx, state.Key)
		if err == nil && active.Active == first.ID {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("monitor did not rollback", monitor.Health())
		}
	}
	if err := monitor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Close(); err != nil {
		t.Fatal("repeated Close", err)
	}
	if checks.Load() != 1 {
		t.Fatal("background validation repeated before close", checks.Load())
	}
	if health := monitor.Health(); health.Validate() != nil {
		t.Fatal(health)
	}
}

func TestSkillRegressionMonitorFailureDoesNotStarveLaterKey(t *testing.T) {
	svc, first, _, state := appRegressionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	store, err := skills.Open(svc.settings.Skills.Root, []string{state.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := first.Draft
	draft.Key.Name = "zz-monitor-later"
	v, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "seed-check", Passed: true, Deterministic: true}, nil
	})
	if err := store.Activate(ctx, draft.Key, v.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	if state.Key.Name >= draft.Key.Name {
		t.Fatal("fixture key not ordered before later key")
	}
	later := make(chan struct{}, 1)
	var active, maxActive atomic.Int32
	validator := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		if v.Draft.Key == state.Key {
			return skills.Evidence{}, errors.New("private first-key failure")
		}
		select {
		case later <- struct{}{}:
		default:
		}
		return skills.Evidence{ID: "later-check", Passed: true, Deterministic: true}, nil
	})
	monitor, err := StartSkillRegression(ctx, svc, time.Second, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	select {
	case <-later:
	case <-ctx.Done():
		t.Fatal("bad first key starved later skill", monitor.Health())
	}
	// Wait until the successful callback is joined before reading sticky health.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for active.Load() != 0 {
		select {
		case <-time.After(time.Millisecond):
		case <-deadline.C:
			t.Fatal("callback did not join")
		}
	}
	if health := monitor.Health(); health.Status != "degraded" {
		t.Fatal("successful later key hid failure", health)
	}
	_ = monitor.Close()
	if active.Load() != 0 || maxActive.Load() != 1 {
		t.Fatal("monitor callbacks overlapped or survived Close")
	}
}

func TestSkillRegressionMonitorAdmission(t *testing.T) {
	svc, _, _, _ := appRegressionFixture(t)
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		t.Fatal("invalid configuration invoked validator")
		return skills.Evidence{}, nil
	})
	for _, interval := range []time.Duration{0, time.Millisecond, 25 * time.Hour} {
		if m, err := StartSkillRegression(context.Background(), svc, interval, validator); err == nil {
			m.Close()
			t.Fatal("invalid interval accepted")
		}
	}
	var empty skills.ValidatorFunc
	for _, v := range []skills.Validator{nil, empty} {
		if _, err := svc.SkillRegressionStep(context.Background(), "", v); !errors.Is(err, ErrAdmission) {
			t.Fatal(err)
		}
		if m, err := StartSkillRegression(context.Background(), svc, time.Second, v); err == nil {
			m.Close()
			t.Fatal("nil validator accepted")
		}
	}
	if _, err := svc.SkillRegressionStep(nil, "", validator); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
	if _, err := svc.SkillRegressionStep(context.Background(), "bad cursor", validator); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
	svc.settings.Skills.Rollback = false
	if _, err := svc.SkillRegressionStep(context.Background(), "", validator); !errors.Is(err, ErrAdmission) {
		t.Fatal("rollback-disabled step admitted", err)
	}
	if m, err := StartSkillRegression(context.Background(), svc, time.Second, validator); err == nil {
		m.Close()
		t.Fatal("rollback-disabled monitor admitted")
	}
}

func TestSkillRegressionMonitorCloseCancelsAndJoinsCallback(t *testing.T) {
	svc, _, _, state := appRegressionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	validator := skills.ValidatorFunc(func(call context.Context, _ skills.Version) (skills.Evidence, error) {
		close(entered)
		<-call.Done()
		close(canceled)
		<-release
		return skills.Evidence{}, call.Err()
	})
	monitor, err := StartSkillRegression(ctx, svc, time.Second, validator)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
		_ = monitor.Close()
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("callback not entered")
	}
	closed := make(chan error, 1)
	go func() { closed <- monitor.Close() }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("Close did not cancel callback")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before callback joined")
	default:
	}
	close(release)
	released = true
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("Close did not join callback")
	}
	current, err := svc.SkillActivationState(context.Background(), state.Key)
	if err != nil || current != state {
		t.Fatal("canceled validation changed activation", current, err)
	}
	if health := monitor.Health(); health.Component != "skill_regression" || health.Validate() != nil {
		t.Fatal(health)
	}
}

func TestSkillRegressionStepSecretCursorAndExactScope(t *testing.T) {
	svc, _, _, state := appRegressionFixture(t)
	ctx := context.Background()
	var calls atomic.Int32
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls.Add(1)
		return skills.Evidence{ID: "check", Passed: true, Deterministic: true}, nil
	})
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-cursor"
		}
		return ""
	}
	if next, err := svc.SkillRegressionStep(ctx, "private-cursor", validator); next != "" || !errors.Is(err, ErrAdmission) || calls.Load() != 0 {
		t.Fatal("secret cursor returned", next, err)
	}
	svc.secret = nil
	svc.settings.Skills.Scope = state.Key.Scope + "-other"
	if next, err := svc.SkillRegressionStep(ctx, "", validator); err != nil || next != "" || calls.Load() != 0 {
		t.Fatal("scope isolation failed", next, err)
	}
}
