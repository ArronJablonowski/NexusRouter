package v1_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKConfiguredLearningDisabledAndGuards(t *testing.T) {
	ctx := context.Background()
	for _, c := range []*sdk.Client{nil, {}} {
		if got, err := c.StartConfiguredLearning(ctx, nil); got != nil || err != sdk.ErrAdmission {
			t.Fatal(got, err)
		}
	}
	for _, s := range []*sdk.ConfiguredLearningSupervisor{nil, {}} {
		if s.Close() != nil || s.Health() != nil {
			t.Fatal("invalid wrapper")
		}
	}
	options, path := sdkToolOptions(t)
	options.Overrides = map[string]string{"skills.learning.enabled": "false", "skills.learning.validator_id": "not-installed-v1"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.StartConfiguredLearning(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checks := s.Health()
	if len(checks) != 1 || checks[0].Component != "learning" || checks[0].Status != "disabled" || checks[0].Validate() != nil {
		t.Fatal(checks)
	}
	checks[0].Status = "changed"
	if s.Health()[0].Status != "disabled" {
		t.Fatal("shared health slice")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled start created storage")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, ctx := range []context.Context{nil, canceled} {
		if got, err := c.StartConfiguredLearning(ctx, nil); got != nil || err != sdk.ErrAdmission {
			t.Fatal(got, err)
		}
	}
}

func TestSDKConfiguredLearningPreflightWithoutDispatch(t *testing.T) {
	for _, mode := range []string{"missing-validator", "missing-catalog"} {
		t.Run(mode, func(t *testing.T) {
			options, path := sdkToolOptions(t)
			root := filepath.Join(t.TempDir(), "missing-catalog")
			options.Overrides = map[string]string{
				"skills.root": root, "skills.scope": "project",
				"skills.learning.enabled": "true", "skills.learning.model_id": "chat",
				"skills.learning.validator_id":     "test-validator-v1",
				"skills.generation_budget.enabled": "true",
			}
			if mode == "missing-catalog" {
				options.Overrides["skills.learning.regression_name"] = "learner-regression"
				options.Overrides["skills.learning.regression_interval"] = "1s"
			}
			c, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var registry *sdk.SkillValidatorRegistry
			if mode == "missing-catalog" {
				registry, err = sdk.NewSkillValidatorRegistry(map[string]skills.Validator{"test-validator-v1": skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
					calls.Add(1)
					return skills.Evidence{}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := c.StartConfiguredLearning(context.Background(), registry)
			if got != nil || err != sdk.ErrAdmission || calls.Load() != 0 {
				t.Fatal(got, err, calls.Load())
			}
			for _, p := range []string{path, root} {
				if _, err = os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("preflight mutated missing storage", err)
				}
			}
		})
	}
}
