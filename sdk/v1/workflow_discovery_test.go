package v1_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKWorkflowDiscoveryReadOnlyAndClientGuards(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		if page, err := client.DiscoverSkillWorkflows(context.Background(), "creative", "", 5); err != sdk.ErrAdmission || !reflect.DeepEqual(page, skills.WorkflowCandidatePage{}) {
			t.Fatal(page, err)
		}
	}
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "unopened")
	options.Overrides = map[string]string{"skills.enabled": "true", "skills.auto_draft": "true", "skills.root": root, "skills.scope": "project"}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := client.DiscoverSkillWorkflows(context.Background(), "creative", "", 5); err != sdk.ErrAdmission || !reflect.DeepEqual(page, skills.WorkflowCandidatePage{}) {
		t.Fatal("missing storage accepted", page, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing database created", err)
	}
	store, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := client.DiscoverSkillWorkflows(context.Background(), "creative", "", 5)
	if err != nil || page.Validate("", 5) != nil {
		t.Fatal("empty database discovery failed", page, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if page, err := client.DiscoverSkillWorkflows(ctx, "creative", "", 5); err != sdk.ErrAdmission || !reflect.DeepEqual(page, skills.WorkflowCandidatePage{}) {
			t.Fatal("invalid context accepted", page, err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("skill root created", err)
	}
}
