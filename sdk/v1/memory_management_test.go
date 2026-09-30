package v1_test

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKMemoryManagementExistingScopedStorage(t *testing.T) {
	options, path := sdkToolOptions(t)
	options.Overrides = map[string]string{"memory.scope": "project", "memory.enabled": "false"}
	options.LookupSecret = func(string) string { return "private-secret" }
	var calls atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		calls.Add(1)
		return nil, sdk.ErrAdmission
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	f := sdk.MemoryFact{Version: 1, ID: "fact", Scope: "project", Revision: 1, Content: "private-secret preference", Provenance: "operator", Confidence: 1, Privacy: "local_only", Created: now, Updated: now}
	if client.PutMemory(ctx, f, 0) != sdk.ErrAdmission {
		t.Fatal("missing storage created")
	}
	if _, err := client.Memories(ctx, "", "", 10, false); err != sdk.ErrAdmission {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing DB created")
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	got, err := client.Memory(ctx, f.ID)
	if err != nil || got.Content != "[REDACTED] preference" {
		t.Fatal(got, err)
	}
	page, err := client.Memories(ctx, "", "preference", 10, false)
	if err != nil || len(page) != 1 || strings.Contains(page[0].Content, "private-secret") {
		t.Fatal(page, err)
	}
	got.Revision = 2
	got.Updated = now.Add(time.Second)
	got.Content = "corrected"
	if err := client.PutMemory(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	if client.DeleteMemory(ctx, f.ID, 1) != sdk.ErrMemoryConflict {
		t.Fatal("stale delete allowed")
	}
	if err := client.DeleteMemory(ctx, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Memory(ctx, f.ID); err != sdk.ErrMemoryConflict {
		t.Fatal("deleted fact found")
	}
	if calls.Load() != 0 {
		t.Fatal("management invoked inference")
	}
}

func TestSDKMemoryManagementClientGuards(t *testing.T) {
	for _, c := range []*sdk.Client{nil, {}} {
		if _, err := c.Memory(context.Background(), "id"); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
		if _, err := c.Memories(context.Background(), "", "", 1, false); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
		if c.PutMemory(context.Background(), sdk.MemoryFact{}, 0) != sdk.ErrAdmission || c.DeleteMemory(context.Background(), "id", 1) != sdk.ErrAdmission {
			t.Fatal("invalid client accepted")
		}
	}
	options, _ := sdkToolOptions(t)
	options.Overrides = map[string]string{"memory.scope": "project"}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := c.Memory(ctx, "id"); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
		if c.DeleteMemory(ctx, "id", 1) != sdk.ErrAdmission {
			t.Fatal("canceled mutation accepted")
		}
	}
}
