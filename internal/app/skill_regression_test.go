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

func appRegressionFixture(t *testing.T) (*Service, skills.Version, skills.Version, skills.ActivationState) {
	t.Helper()
	svc, a := publicationFixture(t)
	ctx := context.Background()
	first, err := svc.PublishSkillGeneration(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "objective-check", Passed: true, Deterministic: true}, nil
	})
	state, err := svc.SkillActivationState(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ActivateSkillVersion(ctx, state, first.ID, pass); err != nil {
		t.Fatal(err)
	}
	store, err := skills.Open(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	draft := first.Draft
	draft.Steps = []string{"Revised workflow"}
	second, err := store.Draft(ctx, draft, false)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	state, err = svc.SkillActivationState(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ActivateSkillVersion(ctx, state, second.ID, pass); err != nil {
		t.Fatal(err)
	}
	state, err = svc.SkillActivationState(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	return svc, first, second, state
}

func TestSkillRegressionPassingAndFailedProofAcrossRestart(t *testing.T) {
	svc, first, second, state := appRegressionFixture(t)
	ctx := context.Background()
	svc.settings.Skills.AutoActivate = false
	path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, passed := range []bool{true, false} {
		proof := skills.Evidence{ID: "regression-check", Passed: passed, Deterministic: true}
		result, err := svc.RevalidateSkillVersion(ctx, state, skills.ValidatorFunc(func(ctx context.Context, v skills.Version) (skills.Evidence, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 3*time.Second {
				t.Error("unbounded regression")
			}
			if !reflect.DeepEqual(v, second) {
				t.Error("wrong active version")
			}
			return proof, nil
		}))
		if err != nil || result.State != state || result.Evidence != proof || result.RolledBack == passed {
			t.Fatal(result, err)
		}
		if passed {
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("passing validation changed catalog", err)
			}
		}
	}
	restarted, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restarted.SkillActivationState(ctx, state.Key)
	if err != nil || current.Active != first.ID || current.Revision == state.Revision {
		t.Fatal(current, err)
	}
	store, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.History(ctx, state.Key)
	if err != nil || len(history.Activations) != 3 {
		t.Fatal(history, err)
	}
	last := history.Activations[2]
	if !last.Rollback || last.Regression == nil || last.Regression.ID != "regression-check" || last.Regression.Passed || last.From != second.ID || last.To != first.ID {
		t.Fatal("missing rollback proof", last)
	}
	loaded, err := store.Load(ctx, state.Key, "")
	if err != nil || !reflect.DeepEqual(loaded, first) {
		t.Fatal("prior workflow not restored", loaded, err)
	}
}

func TestSkillRegressionAdmissionAndEvidenceFailures(t *testing.T) {
	for _, mode := range []string{"rollback-disabled", "disabled", "scope", "injected", "nil-context", "nil-validator", "canceled", "stale", "judge-only", "error", "panic", "new-secret", "old-secret"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _, state := appRegressionFixture(t)
			ctx := context.Background()
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			var validator skills.Validator = skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				calls++
				proof := skills.Evidence{ID: "proof", Passed: false, Deterministic: true}
				switch mode {
				case "judge-only":
					proof.Deterministic = false
				case "error":
					return proof, errors.New("private-regression-error")
				case "panic":
					panic("private-regression-error")
				case "new-secret":
					svc.secret = func(name string) string {
						if name == "DARWIN_API_TOKEN" {
							return "proof"
						}
						return ""
					}
				case "old-secret":
					proof.ID = "runtime-token"
					svc.secret = func(name string) string {
						if name == "DARWIN_API_TOKEN" {
							return "new-secret"
						}
						return ""
					}
				}
				return proof, nil
			})
			switch mode {
			case "rollback-disabled":
				svc.settings.Skills.Rollback = false
			case "disabled":
				svc.settings.Skills.Enabled = false
			case "scope":
				state.Key.Scope = "other"
			case "injected":
				svc.skillStore, _ = contextSkillStore(t)
			case "nil-context":
				ctx = nil
			case "nil-validator":
				validator = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "stale":
				state.Revision = strings.Repeat("a", 64)
			case "old-secret":
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return "runtime-token"
					}
					return ""
				}
			}
			result, err := svc.RevalidateSkillVersion(ctx, state, validator)
			if err == nil || !reflect.DeepEqual(result, skills.RegressionResult{}) || strings.Contains(err.Error(), "private-regression-error") {
				t.Fatal("unsafe failed revalidation", result, err)
			}
			want := 0
			switch mode {
			case "judge-only", "error", "panic", "new-secret", "old-secret":
				want = 1
			}
			if calls != want {
				t.Fatal("wrong callback count", calls, want)
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected revalidation mutated catalog", err)
			}
		})
	}
}
