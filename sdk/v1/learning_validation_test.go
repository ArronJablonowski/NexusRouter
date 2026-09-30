package v1_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKLearningValidationInvalidClients(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		if _, err := client.LearningStepWithValidation(context.Background(), "validator-v1", nil); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
		if supervisor, err := client.StartLearningWithValidation(context.Background(), "validator-v1", nil); !errors.Is(err, sdk.ErrAdmission) || supervisor != nil {
			t.Fatal(supervisor, err)
		}
		if _, err := client.SkillLearningActivationIntent(context.Background(), strings.Repeat("a", 64)); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	for _, supervisor := range []*sdk.LearningSupervisor{nil, {}} {
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
		if supervisor.Health().Component != "learning" {
			t.Fatal("invalid supervisor health")
		}
	}
}

func TestSDKLearningActivationIntentReadOnlyScoped(t *testing.T) {
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "unopened-catalog")
	options.Overrides = map[string]string{"skills.enabled": "false", "skills.learning.enabled": "false", "skills.root": root, "skills.scope": "project", "skills.learning.name": "learner"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	selection := strings.Repeat("a", 64)
	if _, err := client.SkillLearningActivationIntent(ctx, selection); err == nil {
		t.Fatal("missing intent invented")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created DB", err)
	}
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	state := skills.LearningState{Version: 1, Scope: "project", Name: "learner", Domain: "code", PolicyDigest: strings.Repeat("b", 64), Revision: 1, Phase: "discover"}
	if err := store.PutLearningState(ctx, state, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	intent := skills.LearningActivationIntent{Version: 1, Scope: state.Scope, Name: state.Name, SelectionID: selection, PolicyDigest: state.PolicyDigest, ValidatorID: "validator-v1", LearningRevision: 1, Expected: skills.ActivationState{Version: 1, Key: skills.Key{Scope: state.Scope, Name: strings.Repeat("c", 64)}, Revision: strings.Repeat("d", 64)}, Candidate: strings.Repeat("e", 32), CreatedAt: time.Now().UTC()}
	if intent.Validate() != nil {
		t.Fatal("invalid fixture")
	}
	body, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO learning_activation_intents(scope,name,selection_id,body) VALUES(?,?,?,?)`, intent.Scope, intent.Name, intent.SelectionID, body); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.SkillLearningActivationIntent(ctx, selection)
	if err != nil || !reflect.DeepEqual(got, intent) {
		t.Fatal(got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, input := range []context.Context{nil, canceled} {
		if _, err := client.SkillLearningActivationIntent(input, selection); err == nil {
			t.Fatal("invalid ctx accepted")
		}
	}
	options.Overrides["skills.scope"] = "other"
	other, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.SkillLearningActivationIntent(ctx, selection); err == nil {
		t.Fatal("cross-scope intent exposed")
	}
	options.Overrides["skills.scope"] = "project"
	options.LookupSecret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "validator-v1"
		}
		return ""
	}
	secret, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secret.SkillLearningActivationIntent(ctx, selection); err == nil || strings.Contains(err.Error(), "validator-v1") {
		t.Fatal("secret intent exposed", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("intent inspection changed DB", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("inspection created catalog", err)
	}
}

func TestSDKLearningValidationDisabledSupervisorAndGates(t *testing.T) {
	options, path := sdkToolOptions(t)
	options.Overrides = map[string]string{"skills.learning.enabled": "false", "skills.auto_activate_after_validation": "true"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	valid := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		calls++
		return skills.Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	state, err := client.LearningStepWithValidation(context.Background(), "validator-v1", valid)
	if err != nil || state.Version != 0 || calls != 0 {
		t.Fatal(state, err, calls)
	}
	supervisor, err := client.StartLearningWithValidation(context.Background(), "validator-v1", valid)
	if err != nil || supervisor == nil || supervisor.Health().Status != "disabled" {
		t.Fatal(supervisor, err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	var typednil skills.ValidatorFunc
	for _, spec := range []struct {
		id        string
		validator skills.Validator
	}{{"", valid}, {strings.Repeat("x", 65), valid}, {"validator-v1", nil}, {"validator-v1", typednil}} {
		if _, err := client.LearningStepWithValidation(context.Background(), spec.id, spec.validator); err == nil {
			t.Fatal("invalid engine accepted")
		}
		if l, err := client.StartLearningWithValidation(context.Background(), spec.id, spec.validator); err == nil || l != nil {
			t.Fatal("invalid supervisor accepted")
		}
	}
	options.Overrides["skills.auto_activate_after_validation"] = "false"
	disabled, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.LearningStepWithValidation(context.Background(), "validator-v1", valid); err == nil {
		t.Fatal("activation gate bypassed")
	}
	if l, err := disabled.StartLearningWithValidation(context.Background(), "validator-v1", valid); err == nil || l != nil {
		t.Fatal("activation supervisor gate bypassed")
	}
	if calls != 0 {
		t.Fatal("disabled learner called validator")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled learning created storage", err)
	}
}

func TestSDKLearningValidationSupervisorStartsAndJoins(t *testing.T) {
	options, path := sdkToolOptions(t)
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.auto_activate_after_validation": "true", "skills.root": filepath.Join(t.TempDir(), "catalog"), "skills.scope": "project", "skills.generation_budget.enabled": "true", "skills.learning.enabled": "true", "skills.learning.name": "learner", "skills.learning.model_id": "chat", "skills.learning.interval": "1h"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		t.Error("initial discover step invoked validator")
		return skills.Evidence{}, errors.New("unexpected validation")
	})
	supervisor, err := client.StartLearningWithValidation(ctx, "validator-v1", validator)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := client.SkillLearningState(ctx)
		if err == nil && state.Phase == "discover" && supervisor.Health().Status == "healthy" {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("learner did not initialize", supervisor.Health(), err)
		}
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if supervisor.Health().Status != "unavailable" {
		t.Fatal("supervisor not joined", supervisor.Health())
	}
}
