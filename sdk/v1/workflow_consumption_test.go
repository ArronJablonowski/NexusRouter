package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKWorkflowConsumptionRestartAndInspection(t *testing.T) {
	options, _ := sdkToolOptions(t)
	calls := 0
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			calls++
			return emit(providers.Chunk{Text: "A short story with a clear ending.", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Domain: "creative", Prompt: "Write a short story"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Feedback(ctx, task.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "unopened")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 20); err != nil {
		t.Fatal(err)
	}
	receipt, err := client.ConsumeSkillWorkflowScan(ctx, "learning", 0)
	if err != nil || receipt.Validate() != nil || receipt.Considered != 1 || receipt.Eligible != 0 || len(receipt.Buckets) != 0 {
		t.Fatal("text-only workflow grouped", receipt, err)
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := client.ConsumeSkillWorkflowScan(ctx, "learning", 0)
	if err != nil || !reflect.DeepEqual(retry, receipt) {
		t.Fatal("restart changed receipt", retry, err)
	}
	options.Overrides["skills.auto_draft"] = "false"
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	head, err := client.SkillWorkflowScanConsumption(ctx, "learning")
	if err != nil || !reflect.DeepEqual(head, receipt) {
		t.Fatal("inspection blocked", head, err)
	}
	buckets, err := client.SkillWorkflowScanBuckets(ctx, "learning", 1, "", 20)
	if err != nil || buckets == nil || len(buckets) != 0 {
		t.Fatal(buckets, err)
	}
	if _, err := client.ConsumeSkillWorkflowScan(ctx, "learning", 1); err == nil {
		t.Fatal("disabled drafting consumed")
	}
	if calls != 1 {
		t.Fatal("consumption called provider", calls)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("consumption opened skill root", err)
	}
}

func TestSDKWorkflowConsumptionInvalidClientsAndMissingStorage(t *testing.T) {
	options, database := sdkToolOptions(t)
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": filepath.Join(t.TempDir(), "unopened"), "skills.scope": "project"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []*sdk.Client{nil, {}, client} {
		for _, callCtx := range []context.Context{nil, ctx, context.Background()} {
			if got, err := c.ConsumeSkillWorkflowScan(callCtx, "learning", 0); !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(got, skills.WorkflowScanConsumption{}) {
				t.Fatal("invalid consume", got, err)
			}
			if got, err := c.SkillWorkflowScanConsumption(callCtx, "learning"); !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(got, skills.WorkflowScanConsumption{}) {
				t.Fatal("invalid inspect", got, err)
			}
			if got, err := c.SkillWorkflowScanBuckets(callCtx, "learning", 1, "", 20); !errors.Is(err, sdk.ErrAdmission) || got != nil {
				t.Fatal("invalid listing", got, err)
			}
		}
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created storage", err)
	}
}
