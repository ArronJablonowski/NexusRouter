package v1_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func sdkOutcomeRollbackEvidence(t *testing.T) (*sdk.Client, sdk.ConfigOptions, string, string, skills.ActivationState, skills.ComparisonSelectionRequest) {
	t.Helper()
	ctx := context.Background()
	options, path := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.root": root, "skills.scope": "project", "skills.outcome_rollback": "true"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	key := skills.Key{Scope: "project", Name: "workflow"}
	draft := skills.Draft{Key: key, Description: "workflow", Tags: []string{"creative"}, SourceSessions: []string{"fixture"}, Steps: []string{"Inspect input"}, ValidationCases: []string{"fixture"}}
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "trusted-fixture", Passed: true, Deterministic: true}, nil
	})
	versions := []skills.Version{}
	metadata := []skills.Metadata{}
	previous := ""
	for i := 0; i < 2; i++ {
		v, err := store.Draft(ctx, draft, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Activate(ctx, key, v.ID, previous, validator, false); err != nil {
			t.Fatal(err)
		}
		m, err := store.Discover(ctx, key.Scope, []string{"creative"}, 1)
		if err != nil || len(m) != 1 {
			t.Fatal(err)
		}
		versions = append(versions, v)
		metadata = append(metadata, m[0])
		previous = v.ID
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := client.SkillActivationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	request := skills.ComparisonSelectionRequest{Version: 1, ModelID: "chat", Domain: "creative", Profile: "default", Name: key.Name, BaselineVersion: versions[0].ID, CandidateVersion: versions[1].ID, Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: .1, Privacy: "local_only", TasksPerVersion: 20}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	execution := routing.Key{Model: "fixture", Provider: "local", Domain: "creative", Profile: "default"}
	for i := 0; i < 40; i++ {
		cohort := i / 20
		id := fmt.Sprintf("task-%02d", i)
		for j, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
			e := runtime.Event{Version: 1, ID: fmt.Sprintf("event-%d-%d", i, j), TaskID: id, SessionID: id, CorrelationID: id, Sequence: int64(j + 1), Time: time.Now().UTC(), Kind: kind}
			if j > 0 {
				e.AttemptID = "attempt"
				e.TurnID = "turn"
			}
			switch kind {
			case runtime.TaskStarted:
				e.Data = runtime.Data{Privacy: "local_only", Domain: "creative", Profile: "default", Messages: []providers.Message{{Role: "user", Content: "private-task"}}, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: key.Scope, Name: key.Name, Version: versions[cohort].ID, Digest: metadata[cohort].Digest}}}}
			case runtime.TurnStarted:
				e.Data = runtime.Data{ModelID: execution.Model, ProviderID: execution.Provider}
			case runtime.TurnCompleted:
				e.Data = runtime.Data{Text: "private-output", FinishReason: "stop"}
			}
			if err := db.Append(ctx, int64(j), e); err != nil {
				t.Fatal(err)
			}
		}
		r := evaluation.Record{Version: 1, ID: fmt.Sprintf("quality-%d", i), TaskID: id, AttemptID: "attempt", Key: execution, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user", Passed: cohort == 0}}, ExecutionSucceeded: true, Time: time.Now().UTC()}
		if err := db.RecordEvaluation(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return client, options, path, root, expected, request
}

func TestSDKOutcomeRollbackReceiptAndRetry(t *testing.T) {
	ctx := context.Background()
	client, options, path, _, expected, request := sdkOutcomeRollbackEvidence(t)
	key := expected.Key
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.OutcomeRollbackOnce(ctx, "sdk-outcome", expected, request)
	if err != nil || receipt.Validate() != nil || receipt.Decision != "rolled_back" || receipt.After.Active != request.BaselineVersion {
		t.Fatal(receipt, err)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.OutcomeRollbackOnce(ctx, "sdk-outcome", expected, request)
	if err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatal("retry changed receipt", err)
	}
	got, err := restarted.OutcomeRollbackOperation(ctx, key, "sdk-outcome")
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("receipt not durable", err)
	}
	intent, err := restarted.OutcomeRollbackIntent(ctx, key, "sdk-outcome")
	if err != nil || intent.Validate() != nil || intent.Expected != expected || intent.OperationID != "sdk-outcome" || intent.ConfiguredModelID != "chat" || intent.Policy != receipt.Policy {
		t.Fatal("operation intent not durable", err)
	}
	checkpoint, err := restarted.OutcomeSelectionCheckpoint(ctx, key, "sdk-outcome")
	if err != nil || checkpoint.Validate() != nil || checkpoint.Intent != intent || !reflect.DeepEqual(checkpoint.Report, receipt.Selection) {
		t.Fatal("selected evidence checkpoint not durable", err)
	}
	checkpoint.Report.Comparison.Excluded["caller-mutation"] = 1
	checkpoint, err = restarted.OutcomeSelectionCheckpoint(ctx, key, "sdk-outcome")
	if err != nil || checkpoint.Validate() != nil || !reflect.DeepEqual(checkpoint.Report, receipt.Selection) {
		t.Fatal("checkpoint inspection returned mutable alias", err)
	}
	state, err := restarted.SkillActivationState(ctx, key)
	if err != nil || state != receipt.After {
		t.Fatal("rollback state differs", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("outcome rollback mutated telemetry", err)
	}
	options.Overrides["skills.outcome_rollback"] = "false"
	disabled, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := disabled.OutcomeRollbackOnce(ctx, "sdk-outcome", expected, request); err == nil || out.Version != 0 {
		t.Fatal("disabled rollback acknowledged mutation")
	}
	got, err = disabled.OutcomeRollbackOperation(ctx, key, "sdk-outcome")
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("disabled inspection unavailable", err)
	}
	inspected, err := disabled.OutcomeRollbackIntent(ctx, key, "sdk-outcome")
	if err != nil || !reflect.DeepEqual(inspected, intent) {
		t.Fatal("disabled intent inspection unavailable", err)
	}
	savedSelection, err := disabled.OutcomeSelectionCheckpoint(ctx, key, "sdk-outcome")
	if err != nil || !reflect.DeepEqual(savedSelection, checkpoint) {
		t.Fatal("disabled checkpoint inspection unavailable", err)
	}
}

func TestSDKOutcomeRollbackGuardsNoCreation(t *testing.T) {
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "absent-catalog")
	options.Overrides = map[string]string{"skills.root": root, "skills.scope": "project"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, client := range []*sdk.Client{nil, {}, c} {
		for _, input := range []context.Context{nil, context.Background(), ctx} {
			if out, err := client.OutcomeRollbackOnce(input, "operation", skills.ActivationState{}, skills.ComparisonSelectionRequest{}); err == nil || out.Version != 0 {
				t.Fatal("invalid mutation admitted")
			}
			if out, err := client.OutcomeRollbackOperation(input, skills.Key{Scope: "project", Name: "workflow"}, "operation"); err == nil || out.Version != 0 {
				t.Fatal("missing receipt admitted")
			}
		}
	}
	if _, err := c.OutcomeRollbackOperation(ctx, skills.Key{Scope: "project", Name: "workflow"}, "operation"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	for _, missing := range []string{path, root} {
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("guard created storage", err)
		}
	}
}
