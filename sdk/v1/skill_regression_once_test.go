package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKSkillRegressionOnceDurableReceiptAndBoundaries(t *testing.T) {
	options, _ := sdkToolOptions(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "catalog")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.rollback_on_regression": "true", "skills.auto_activate_after_validation": "false", "skills.root": root, "skills.scope": "project"}
	store, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := skills.Key{Scope: "project", Name: "workflow"}
	draft := skills.Draft{Key: key, Description: "Workflow", SourceSessions: []string{"session"}, Steps: []string{"Inspect requirements"}, ValidationCases: []string{"Fixture"}}
	pass := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "objective-check", Passed: true, Deterministic: true}, nil
	})
	first, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, key, first.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	second, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, key, second.ID, first.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	store.Close()
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.SkillActivationState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
		calls++
		if v.ID != second.ID {
			t.Error("wrong active version")
		}
		return skills.Evidence{ID: "regression-proof", Passed: false, Deterministic: true}, nil
	})
	receipt, err := client.RevalidateSkillVersionOnce(ctx, "sdk-operation", "sdk-validator", state, validator)
	if err != nil || receipt.OperationID != "sdk-operation" || receipt.ValidatorID != "sdk-validator" || receipt.Expected != state || !receipt.Result.RolledBack || receipt.After.Active != first.ID || calls != 1 {
		t.Fatal(receipt, err, calls)
	}
	restarted, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.RevalidateSkillVersionOnce(ctx, "sdk-operation", "sdk-validator", state, validator)
	if err != nil || !reflect.DeepEqual(again, receipt) || calls != 1 {
		t.Fatal("retry was not receipt-only", err, calls)
	}
	got, err := restarted.SkillRegressionOperation(ctx, key, "sdk-operation")
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("receipt not durable", err)
	}
	current, err := restarted.SkillActivationState(ctx, key)
	if err != nil || current != receipt.After {
		t.Fatal("receipt does not bind post-rollback state", err)
	}
	if _, err := restarted.RevalidateSkillVersionOnce(ctx, "sdk-operation", "different-validator", state, validator); !errors.Is(err, sdk.ErrAdmission) || calls != 1 {
		t.Fatal("validator identity rebound", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, op := range []func(*sdk.Client, context.Context) error{
		func(c *sdk.Client, ctx context.Context) error {
			_, err := c.RevalidateSkillVersionOnce(ctx, "sdk-operation", "sdk-validator", state, validator)
			return err
		},
		func(c *sdk.Client, ctx context.Context) error {
			_, err := c.SkillRegressionOperation(ctx, key, "sdk-operation")
			return err
		},
	} {
		for _, invalid := range []*sdk.Client{nil, {}} {
			if err := op(invalid, ctx); !errors.Is(err, sdk.ErrAdmission) {
				t.Fatal("invalid client", err)
			}
		}
		if err := op(client, nil); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal("nil context", err)
		}
		if err := op(client, canceled); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity", err)
		}
	}
	options.Overrides["skills.rollback_on_regression"] = "false"
	readonly, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	got, err = readonly.SkillRegressionOperation(ctx, key, "sdk-operation")
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("disabled rollback prevented inspection", err)
	}
	if _, err := readonly.RevalidateSkillVersionOnce(ctx, "sdk-operation", "sdk-validator", state, validator); !errors.Is(err, sdk.ErrAdmission) || calls != 1 {
		t.Fatal("disabled policy allowed retry", err)
	}
	missing := filepath.Join(parent, "missing")
	options.Overrides["skills.root"] = missing
	missingClient, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missingClient.SkillRegressionOperation(ctx, key, "sdk-operation"); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal("missing catalog", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection created catalog", err)
	}
	if calls != 1 {
		t.Fatal("boundary operations invoked validator", calls)
	}
}
