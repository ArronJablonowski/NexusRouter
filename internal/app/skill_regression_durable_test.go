package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestDurableSkillRegressionPendingReceiptRestartAndCadence(t *testing.T) {
	svc, _, second, state := appRegressionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var operation string
	calls := 0
	validator := skills.ValidatorFunc(func(callCtx context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		if v.ID != second.ID {
			t.Error("wrong active candidate")
		}
		pending, err := svc.SkillRegressionMonitorState(callCtx, "monitor")
		if err != nil || pending.PendingOperationID == "" {
			t.Error("callback ran before durable pending state", err)
			return skills.Evidence{}, errors.New("missing pending")
		}
		operation = pending.PendingOperationID
		check, err := svc.SkillRegressionMonitorCheck(callCtx, "monitor", operation)
		if err != nil || check.Status != "pending" || check.Expected != state || check.ValidatorID != "objective-validator" {
			t.Error("callback missing bound pending check", err)
		}
		return skills.Evidence{ID: "objective-proof", Passed: true, Deterministic: true}, nil
	})
	result, err := svc.DurableSkillRegressionStep(ctx, "monitor", "objective-validator", time.Hour, validator)
	if err != nil || calls != 1 || result.After != state.Key.Name || result.PendingOperationID != "" || result.NextDue.IsZero() {
		t.Fatal("step did not finish with durable cadence", result, err, calls)
	}
	check, err := svc.SkillRegressionMonitorCheck(ctx, "monitor", operation)
	if err != nil || check.Status != "completed" || check.FinishedAt.IsZero() {
		t.Fatal("completed check missing", check, err)
	}
	receipt, err := svc.SkillRegressionOperation(ctx, state.Key, operation)
	if err != nil || receipt.Expected != state || receipt.After != state {
		t.Fatal("monitor did not use receipt-backed check", err)
	}
	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.DurableSkillRegressionStep(ctx, "monitor", "objective-validator", time.Hour, validator)
	if err != nil || !reflect.DeepEqual(again, result) || calls != 1 {
		t.Fatal("restart ignored persisted cadence", err, calls)
	}
	for _, spec := range []struct {
		validator string
		interval  time.Duration
	}{{"changed-validator", time.Hour}, {"objective-validator", 2 * time.Hour}} {
		if _, err := restarted.DurableSkillRegressionStep(ctx, "monitor", spec.validator, spec.interval, validator); !errors.Is(err, ErrAdmission) || calls != 1 {
			t.Fatal("monitor identity rebound", err)
		}
	}
	restarted.settings.Skills.Rollback = false
	got, err := restarted.SkillRegressionMonitorState(ctx, "monitor")
	if err != nil || !reflect.DeepEqual(got, result) {
		t.Fatal("disabled rollback hid state", err)
	}
	gotCheck, err := restarted.SkillRegressionMonitorCheck(ctx, "monitor", operation)
	if err != nil || !reflect.DeepEqual(gotCheck, check) {
		t.Fatal("disabled rollback hid check", err)
	}
}

func TestDurableSkillRegressionFailureIsInspectableAndAdvances(t *testing.T) {
	svc, _, second, _ := appRegressionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := skills.Open(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	draft := second.Draft
	draft.Key.Name = "zeta"
	next, err := store.Draft(ctx, draft, false)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	if err := store.Activate(ctx, draft.Key, next.ID, "", pass, false); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	var operation string
	calls := 0
	validator := skills.ValidatorFunc(func(callCtx context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		pending, err := svc.SkillRegressionMonitorState(callCtx, "fair-monitor")
		if err != nil {
			return skills.Evidence{}, err
		}
		operation = pending.PendingOperationID
		if v.Draft.Key.Name == "workflow" {
			return skills.Evidence{}, errors.New("private-validator-failure")
		}
		return skills.Evidence{ID: "next-proof", Passed: true, Deterministic: true}, nil
	})
	failed, err := svc.DurableSkillRegressionStep(ctx, "fair-monitor", "validator", time.Second, validator)
	if !errors.Is(err, ErrAdmission) || calls != 1 || failed.After != "workflow" || failed.PendingOperationID != "" {
		t.Fatal("failed check did not advance durable cursor", failed, err, calls)
	}
	check, err := svc.SkillRegressionMonitorCheck(ctx, "fair-monitor", operation)
	if err != nil || check.Status != "failed" || check.Code == "" || strings.Contains(check.Code, "private") || check.FinishedAt.IsZero() {
		t.Fatal("unsafe failure metadata", check, err)
	}
	// Wait only for the persisted cadence, with the test-wide deadline bounding it.
	wait := time.Until(failed.NextDue) + 10*time.Millisecond
	if wait > 2*time.Second {
		t.Fatal("unexpected cadence", wait)
	}
	if wait < 0 {
		wait = 0
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restarted.DurableSkillRegressionStep(ctx, "fair-monitor", "validator", time.Second, validator)
	if err != nil || calls != 2 || after.After != "zeta" {
		t.Fatal("failure starved next skill", after, err, calls)
	}
}

func TestDurableSkillRegressionSecretAdmissionAndMissingInspection(t *testing.T) {
	for _, field := range []string{"name", "validator"} {
		t.Run(field, func(t *testing.T) {
			svc, _, _, _ := appRegressionFixture(t)
			name, id := "monitor-name", "validator-name"
			secret := name
			if field == "validator" {
				secret = id
			}
			svc.secret = func(key string) string {
				if key == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				calls++
				return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
			})
			if _, err := svc.DurableSkillRegressionStep(context.Background(), name, id, time.Second, validator); !errors.Is(err, ErrAdmission) || calls != 0 {
				t.Fatal("secret monitor admitted", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("secret admission changed catalog", err)
			}
		})
	}
	svc, _ := publicationFixture(t)
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "missing")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, context.Background()} {
		if _, err := svc.SkillRegressionMonitorState(ctx, "monitor"); !errors.Is(err, ErrAdmission) {
			t.Fatal(err)
		}
		if _, err := svc.SkillRegressionMonitorCheck(ctx, "monitor", "operation"); !errors.Is(err, ErrAdmission) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection created root", err)
	}
}

func TestDurableSkillRegressionRestartResumesPreparedCheck(t *testing.T) {
	svc, _, _, state := appRegressionFixture(t)
	ctx := context.Background()
	digest, err := svc.regressionMonitorPolicy("prepared-monitor", "validator", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store, err := skills.Open(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAutomatic(true)
	prepared, check, err := store.PrepareRegressionMonitor(ctx, "project", "prepared-monitor", "validator", digest, time.Hour, func(context.Context, skills.RegressionMonitorState, skills.RegressionMonitorCheck) error { return nil })
	store.Close()
	if err != nil || prepared.PendingOperationID == "" || check.Expected != state {
		t.Fatal("failed to seed pending check", err)
	}
	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(callCtx context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		pending, err := restarted.SkillRegressionMonitorState(callCtx, "prepared-monitor")
		if err != nil || pending.PendingOperationID != prepared.PendingOperationID || v.ID != state.Active {
			t.Error("restart replaced pending work", err)
		}
		return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	finished, err := restarted.DurableSkillRegressionStep(ctx, "prepared-monitor", "validator", time.Hour, validator)
	if err != nil || calls != 1 || finished.PendingOperationID != "" || finished.After != state.Key.Name {
		t.Fatal("pending check not resumed", finished, err)
	}
	got, err := restarted.SkillRegressionMonitorCheck(ctx, "prepared-monitor", prepared.PendingOperationID)
	if err != nil || got.Status != "completed" || got.OperationID != check.OperationID || got.Sequence != check.Sequence || !got.PreparedAt.Equal(check.PreparedAt) {
		t.Fatal("pending identity changed on resume", got, err)
	}
}

func TestDurableSkillRegressionSecretRotationDoesNotPublishProof(t *testing.T) {
	svc, _, _, state := appRegressionFixture(t)
	ctx := context.Background()
	operation := ""
	validator := skills.ValidatorFunc(func(callCtx context.Context, _ skills.Version) (skills.Evidence, error) {
		pending, err := svc.SkillRegressionMonitorState(callCtx, "rotation-monitor")
		if err != nil {
			return skills.Evidence{}, err
		}
		operation = pending.PendingOperationID
		svc.secret = func(name string) string {
			if name == "DARWIN_API_TOKEN" {
				return "new-private-proof"
			}
			return ""
		}
		return skills.Evidence{ID: "new-private-proof", Passed: true, Deterministic: true}, nil
	})
	_, err := svc.DurableSkillRegressionStep(ctx, "rotation-monitor", "validator", time.Hour, validator)
	if !errors.Is(err, ErrAdmission) || operation == "" {
		t.Fatal("rotated secret proof accepted", err)
	}
	current, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil || current != state {
		t.Fatal("secret proof changed activation", err)
	}
	if _, err := svc.SkillRegressionOperation(ctx, state.Key, operation); !errors.Is(err, ErrAdmission) {
		t.Fatal("secret proof receipt published", err)
	}
	body, err := os.ReadFile(filepath.Join(svc.settings.Skills.Root, "catalog.json"))
	if err != nil || strings.Contains(string(body), "new-private-proof") {
		t.Fatal("secret proof persisted", err)
	}
}
