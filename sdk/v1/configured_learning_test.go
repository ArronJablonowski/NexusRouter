package v1_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
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

func TestSDKDirectValidatorCallbacksCannotClaimProtectedIdentity(t *testing.T) {
	options, _ := sdkToolOptions(t)
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls.Add(1)
		return skills.Evidence{ID: sdk.ObservedToolsActivationValidatorID, Passed: true, Deterministic: true}, nil
	})
	ctx := context.Background()
	if _, err = c.LearningStepWithValidation(ctx, sdk.ObservedToolsActivationValidatorID, validator); err != sdk.ErrAdmission {
		t.Fatal("direct learning claimed protected identity", err)
	}
	if learner, startErr := c.StartLearningWithValidation(ctx, sdk.ObservedToolsActivationValidatorID, validator); startErr != sdk.ErrAdmission || learner != nil {
		t.Fatal("direct learner claimed protected identity", learner, startErr)
	}
	if _, err = c.RevalidateSkillVersionOnce(ctx, "operation", sdk.ObservedToolsActivationValidatorID, skills.ActivationState{}, validator); err != sdk.ErrAdmission {
		t.Fatal("direct revalidation claimed protected identity", err)
	}
	if _, err = c.DurableSkillRegressionStep(ctx, "monitor", sdk.ObservedToolsActivationValidatorID, time.Second, validator); err != sdk.ErrAdmission {
		t.Fatal("direct regression step claimed protected identity", err)
	}
	if monitor, startErr := c.StartDurableSkillRegression(ctx, "monitor", sdk.ObservedToolsActivationValidatorID, time.Second, validator); startErr != sdk.ErrAdmission || monitor != nil {
		t.Fatal("direct regression monitor claimed protected identity", monitor, startErr)
	}
	if calls.Load() != 0 {
		t.Fatal("protected identity invoked host callback", calls.Load())
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

func TestSDKConfiguredLearningExposesProtectedObservedToolsOptIn(t *testing.T) {
	if sdk.ObservedToolsActivationValidatorID != "darwin_observed_tools_activation_v1" {
		t.Fatal("unstable stock validator identity", sdk.ObservedToolsActivationValidatorID)
	}
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "missing-catalog")
	options.Overrides = map[string]string{
		"skills.root":                      root,
		"skills.scope":                     "project",
		"skills.learning.enabled":          "true",
		"skills.learning.model_id":         "chat",
		"skills.learning.validator_id":     sdk.ObservedToolsActivationValidatorID,
		"skills.generation_budget.enabled": "true",
	}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := c.ConfiguredSkillValidatorRegistry(nil)
	if err != nil || prepared == nil {
		t.Fatal("SDK did not expose configured validator builder", err)
	}
	if _, err = prepared.Resolve(sdk.ObservedToolsActivationValidatorID); err != nil {
		t.Fatal("SDK builder omitted protected validator", err)
	}
	// The protected callback resolves without a host registry. Startup then
	// fails closed at the absent durable-state preflight without creating it.
	got, err := c.StartConfiguredLearning(context.Background(), prepared)
	if got != nil || err != sdk.ErrAdmission {
		t.Fatal("unexpected protected validator startup", got, err)
	}
	for _, candidate := range []string{path, root} {
		if _, statErr := os.Stat(candidate); !os.IsNotExist(statErr) {
			t.Fatal("preflight mutated missing storage", candidate, statErr)
		}
	}

	spoof, err := sdk.NewSkillValidatorRegistry(map[string]skills.Validator{
		sdk.ObservedToolsActivationValidatorID: skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
			t.Fatal("protected identity invoked host callback")
			return skills.Evidence{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err = c.StartConfiguredLearning(context.Background(), spoof); got != nil || err != sdk.ErrAdmission {
		t.Fatal("SDK accepted protected validator override", got, err)
	}
	if built, buildErr := c.ConfiguredSkillValidatorRegistry(map[string]skills.Validator{
		sdk.ObservedToolsActivationValidatorID: skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
			return skills.Evidence{}, nil
		}),
	}); built != nil || buildErr != sdk.ErrAdmission {
		t.Fatal("SDK builder accepted protected validator override", built, buildErr)
	}

	disabledOptions, _ := sdkToolOptions(t)
	disabled, err := sdk.New(disabledOptions)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := disabled.StartConfiguredLearning(context.Background(), spoof); got != nil || err != sdk.ErrAdmission {
		t.Fatal("disabled SDK accepted protected validator override", got, err)
	}
}
