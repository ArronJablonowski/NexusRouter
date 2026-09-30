package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSDKOutcomeSelectionCheckpointNoCreation(t *testing.T) {
	options, path := sdkToolOptions(t)
	root := filepath.Join(t.TempDir(), "absent-catalog")
	options.Overrides = map[string]string{"skills.root": root, "skills.scope": "project", "skills.enabled": "false"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, client := range []*sdk.Client{nil, {}, c} {
		for _, input := range []context.Context{nil, context.Background(), ctx} {
			if out, err := client.OutcomeSelectionCheckpoint(input, skills.Key{Scope: "project", Name: "workflow"}, "operation"); err == nil || out.Version != 0 {
				t.Fatal("missing checkpoint admitted")
			}
		}
	}
	if _, err := c.OutcomeSelectionCheckpoint(ctx, skills.Key{Scope: "project", Name: "workflow"}, "operation"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	for _, key := range []skills.Key{{Scope: "other", Name: "workflow"}, {Scope: "project", Name: "invalid/name"}} {
		if out, err := c.OutcomeSelectionCheckpoint(context.Background(), key, "operation"); err == nil || out.Version != 0 {
			t.Fatal("invalid key admitted")
		}
	}
	for _, missing := range []string{path, root} {
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("inspection created storage", err)
		}
	}
}
