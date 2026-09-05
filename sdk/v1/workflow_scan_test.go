package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKWorkflowScanDurablePages(t *testing.T) {
	options, _ := sdkToolOptions(t)
	var calls atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			calls.Add(1)
			return emit(providers.Chunk{Text: "A short story with a clear ending.", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 2 {
		task, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Domain: "creative", Prompt: "Write a short story"})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Feedback(ctx, task.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(t.TempDir(), "unopened")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 1)
	if err != nil || first.Validate() != nil || len(first.Page.Candidates) != 1 || first.Scan.Complete {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := client.AdvanceSkillWorkflowScan(ctx, "learning", "creative", first.Scan.Revision, 1)
	if err != nil || second.Validate() != nil || !second.Scan.Complete || second.Scan.Revision != 2 {
		t.Fatalf("second page: %+v %v", second, err)
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := client.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 0, 1)
	if err != nil || !reflect.DeepEqual(repeated, first) {
		t.Fatalf("historical retry: %+v %v", repeated, err)
	}
	head, err := client.SkillWorkflowScan(ctx, "learning")
	if err != nil || head != second.Scan {
		t.Fatalf("head: %+v %v", head, err)
	}
	options.Overrides["skills.auto_draft"] = "false"
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if head, err := client.SkillWorkflowScan(ctx, "learning"); err != nil || head != second.Scan {
		t.Fatal("disabled drafting blocked inspection", err)
	}
	if _, err := client.AdvanceSkillWorkflowScan(ctx, "learning", "creative", 2, 1); err == nil {
		t.Fatal("disabled drafting advanced")
	}
	if calls.Load() != 2 {
		t.Fatal("scan invoked provider", calls.Load())
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scan opened skill root", err)
	}
}

func TestSDKWorkflowScanGuardsAndMissingStorage(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		if page, err := client.AdvanceSkillWorkflowScan(context.Background(), "learning", "creative", 0, 1); !errors.Is(err, sdk.ErrAdmission) || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) {
			t.Fatal("invalid client advanced", err)
		}
		if head, err := client.SkillWorkflowScan(context.Background(), "learning"); !errors.Is(err, sdk.ErrAdmission) || head != (skills.WorkflowScan{}) {
			t.Fatal("invalid client inspected", err)
		}
	}
	options, database := sdkToolOptions(t)
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": filepath.Join(t.TempDir(), "unopened"), "skills.scope": "project"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, callCtx := range []context.Context{nil, ctx, context.Background()} {
		if page, err := client.AdvanceSkillWorkflowScan(callCtx, "learning", "creative", 0, 1); err == nil || !reflect.DeepEqual(page, skills.WorkflowScanPage{}) {
			t.Fatal("invalid context/storage advanced", err)
		}
		if head, err := client.SkillWorkflowScan(callCtx, "learning"); err == nil || head != (skills.WorkflowScan{}) {
			t.Fatal("invalid context/storage inspected", err)
		}
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scan created database", err)
	}
}
