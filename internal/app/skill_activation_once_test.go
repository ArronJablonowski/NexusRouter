package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillActivationOnceRetryAndPolicy(t *testing.T) {
	svc, a := publicationFixture(t)
	ctx := context.Background()
	v, err := svc.PublishSkillGeneration(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.SkillActivationState(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls++
		return skills.Evidence{ID: "objective-check", Passed: true, Deterministic: true}, nil
	})
	if err := svc.ActivateSkillVersionOnce(ctx, "operation-a", state, v.ID, validator); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	receipt, err := svc.SkillActivationOperation(ctx, a.Key, "operation-a")
	if err != nil || receipt.OperationID != "operation-a" || receipt.Expected != state || receipt.Candidate != v.ID {
		t.Fatal(receipt, err)
	}
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.ActivateSkillVersionOnce(ctx, "operation-a", state, v.ID, validator); err != nil || calls != 1 {
		t.Fatal("repeat invoked validator", err, calls)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("receipt retry mutated catalog", err)
	}
	changed := state
	changed.Revision = strings.Repeat("a", 64)
	for _, spec := range []struct {
		op    string
		state skills.ActivationState
		id    string
	}{{"operation-a", changed, v.ID}, {"operation-a", state, strings.Repeat("b", 32)}, {"operation-b", state, v.ID}, {strings.Repeat("x", 65), state, v.ID}} {
		if err := reopened.ActivateSkillVersionOnce(ctx, spec.op, spec.state, spec.id, validator); !errors.Is(err, ErrAdmission) || calls != 1 {
			t.Fatal("conflict admitted", err, calls)
		}
	}
	for _, mode := range []string{"disabled", "automatic", "scope", "secret", "typed-nil"} {
		t.Run(mode, func(t *testing.T) {
			copy, err := NewService(reopened.settings, reopened.secret)
			if err != nil {
				t.Fatal(err)
			}
			check := validator
			switch mode {
			case "disabled":
				copy.settings.Skills.Enabled = false
			case "automatic":
				copy.settings.Skills.AutoActivate = false
			case "scope":
				copy.settings.Skills.Scope = "other"
			case "secret":
				copy.secret = func(string) string { return "operation-a" }
			case "typed-nil":
				check = nil
			}
			if err := copy.ActivateSkillVersionOnce(ctx, "operation-a", state, v.ID, check); !errors.Is(err, ErrAdmission) || calls != 1 {
				t.Fatal("policy bypassed on retry", err, calls)
			}
		})
	}
}

func TestSkillActivationOnceRetryDoesNotUndoRollback(t *testing.T) {
	svc, _, second, state := appRegressionFixture(t)
	ctx := context.Background()
	store, err := skills.Open(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	draft := second.Draft
	draft.Steps = []string{"Third workflow"}
	third, err := store.Draft(ctx, draft, false)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls++
		return skills.Evidence{ID: "third-proof", Passed: true, Deterministic: true}, nil
	})
	if err := svc.ActivateSkillVersionOnce(ctx, "third-op", state, third.ID, validator); err != nil {
		t.Fatal(err)
	}
	active, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.RevalidateSkillVersion(ctx, active, skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "regression", Passed: false, Deterministic: true}, nil
	}))
	if err != nil || !result.RolledBack {
		t.Fatal(result, err)
	}
	rolled, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil || rolled.Active != second.ID {
		t.Fatal(rolled, err)
	}
	if err := svc.ActivateSkillVersionOnce(ctx, "third-op", state, third.ID, validator); err != nil || calls != 1 {
		t.Fatal("old operation reexecuted", err, calls)
	}
	after, err := svc.SkillActivationState(ctx, state.Key)
	if err != nil || after != rolled {
		t.Fatal("rollback undone", after, err)
	}
}

func TestSkillActivationOperationInspectionDoesNotCreate(t *testing.T) {
	svc, _ := publicationFixture(t)
	svc.settings.Skills.Root = filepath.Join(t.TempDir(), "missing")
	key := skills.Key{Scope: "project", Name: "workflow"}
	if _, err := svc.SkillActivationOperation(context.Background(), key, "operation"); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection created catalog", err)
	}
	if _, err := svc.SkillActivationOperation(nil, key, "operation"); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
}

func TestSkillActivationOnceSecretRotationRejectsOperationPersistence(t *testing.T) {
	svc, a := publicationFixture(t)
	ctx := context.Background()
	v, err := svc.PublishSkillGeneration(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.SkillActivationState(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		svc.secret = func(name string) string {
			if name == "DARWIN_API_TOKEN" {
				return "rotating-operation"
			}
			return ""
		}
		return skills.Evidence{ID: "objective-check", Passed: true, Deterministic: true}, nil
	})
	if err := svc.ActivateSkillVersionOnce(ctx, "rotating-operation", state, v.ID, validator); !errors.Is(err, ErrAdmission) {
		t.Fatal("operation credential persisted", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("secret rotation changed catalog", err)
	}
	store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ActivationOperation(ctx, state.Key, "rotating-operation"); !errors.Is(err, skills.ErrNotFound) {
		t.Fatal("secret operation receipt persisted", err)
	}
}
