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

func TestSkillRegressionOncePassRollbackAndReceiptRetry(t *testing.T) {
	svc, first, second, state := appRegressionFixture(t)
	ctx := context.Background()
	calls := 0
	passed := true
	validator := skills.ValidatorFunc(func(ctx context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		if v.ID != second.ID {
			t.Error("wrong checked version")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Error("callback lacks bounded deadline")
		}
		return skills.Evidence{ID: "objective-proof", Passed: passed, Deterministic: true}, nil
	})
	pass, err := svc.RevalidateSkillVersionOnce(ctx, "pass-op", "fixture-validator", state, validator)
	if err != nil || pass.Version != 1 || pass.Expected != state || pass.Result.State != state || pass.Result.RolledBack || pass.After != state || pass.ActivationCount != 2 || pass.CheckedAt.IsZero() || calls != 1 {
		t.Fatal("passing receipt invalid", pass, err, calls)
	}
	current, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil || current != state {
		t.Fatal("pass changed activation revision", err)
	}
	passed = false
	rollback, err := svc.RevalidateSkillVersionOnce(ctx, "rollback-op", "fixture-validator", state, validator)
	// ActivationCount identifies the checked pre-operation history prefix.
	if err != nil || !rollback.Result.RolledBack || rollback.Expected != state || rollback.After.Active != first.ID || rollback.After.Revision == state.Revision || rollback.ActivationCount != 2 || calls != 2 {
		t.Fatal("rollback receipt invalid", rollback, err, calls)
	}
	// Change the activation after the receipt; its retry must remain historical.
	good := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "reactivation-proof", Passed: true, Deterministic: true}, nil
	})
	if err := svc.ActivateSkillVersion(ctx, rollback.After, second.ID, good); err != nil {
		t.Fatal(err)
	}
	later, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil || later.Active != second.ID {
		t.Fatal(later, err)
	}
	reopened, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range []skills.RegressionOperation{pass, rollback} {
		got, err := reopened.RevalidateSkillVersionOnce(ctx, receipt.OperationID, "fixture-validator", state, validator)
		if err != nil || !reflect.DeepEqual(got, receipt) || calls != 2 {
			t.Fatal("retry changed receipt or invoked callback", err, calls)
		}
		reopened.settings.Skills.Rollback = false
		got, err = reopened.SkillRegressionOperation(ctx, state.Key, receipt.OperationID)
		if err != nil || !reflect.DeepEqual(got, receipt) {
			t.Fatal("read-only receipt unavailable with rollback disabled", err)
		}
		reopened.settings.Skills.Rollback = true
	}
	changed := state
	changed.Revision = strings.Repeat("a", 64)
	for _, spec := range []struct {
		op, validator string
		state         skills.ActivationState
	}{{"rollback-op", "other-validator", state}, {"rollback-op", "fixture-validator", changed}, {"new-op", "fixture-validator", state}} {
		if got, err := reopened.RevalidateSkillVersionOnce(ctx, spec.op, spec.validator, spec.state, validator); !errors.Is(err, ErrAdmission) || !reflect.DeepEqual(got, skills.RegressionOperation{}) || calls != 2 {
			t.Fatal("rebound or stale operation admitted", err, calls)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("receipt inspection/retry/conflict changed catalog", err)
	}
	current, err = reopened.SkillActivationState(ctx, state.Key)
	if err != nil || current != later {
		t.Fatal("historical retry undid later activation", err)
	}
}

func TestSkillRegressionOnceAdmissionAndSecretRotation(t *testing.T) {
	for _, mode := range []string{"operation-secret", "validator-secret", "operation-rotates", "validator-rotates", "old-proof-secret", "nil-context", "canceled", "nil-validator", "rollback-disabled"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _, state := appRegressionFixture(t)
			ctx := context.Background()
			operation, validatorID := "regression-operation", "regression-validator"
			setSecret := func(value string) {
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return value
					}
					return ""
				}
			}
			calls := 0
			var validator skills.Validator = skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				calls++
				proof := skills.Evidence{ID: "proof", Passed: false, Deterministic: true}
				switch mode {
				case "operation-rotates":
					setSecret(operation)
				case "validator-rotates":
					setSecret(validatorID)
				case "old-proof-secret":
					proof.ID = "old-credential"
					setSecret("new-credential")
				}
				return proof, nil
			})
			switch mode {
			case "operation-secret":
				setSecret(operation)
			case "validator-secret":
				setSecret(validatorID)
			case "old-proof-secret":
				setSecret("old-credential")
			case "nil-context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-validator":
				validator = nil
			case "rollback-disabled":
				svc.settings.Skills.Rollback = false
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := svc.RevalidateSkillVersionOnce(ctx, operation, validatorID, state, validator)
			if !errors.Is(err, ErrAdmission) || !reflect.DeepEqual(got, skills.RegressionOperation{}) {
				t.Fatal("invalid operation admitted", err)
			}
			want := 0
			if mode == "operation-rotates" || mode == "validator-rotates" || mode == "old-proof-secret" {
				want = 1
			}
			if calls != want {
				t.Fatal("callback count", calls, want)
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected operation mutated catalog", err)
			}
		})
	}
}

func TestSkillRegressionOperationReadOnlyMissingRootAndContext(t *testing.T) {
	svc, _ := publicationFixture(t)
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "missing")
	key := skills.Key{Scope: "project", Name: "workflow"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, context.Background()} {
		got, err := svc.SkillRegressionOperation(ctx, key, "operation")
		if !errors.Is(err, ErrAdmission) || !reflect.DeepEqual(got, skills.RegressionOperation{}) {
			t.Fatal("invalid inspection", err)
		}
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection created missing root", err)
	}
}
