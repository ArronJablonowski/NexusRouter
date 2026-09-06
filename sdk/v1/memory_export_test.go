package v1_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKMemoryExportExistingScopedSnapshot(t *testing.T) {
	options, path := sdkToolOptions(t)
	options.Overrides = map[string]string{"memory.scope": "project", "memory.enabled": "false"}
	options.LookupSecret = func(string) string { return "private-secret" }
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = c.ExportMemory(ctx); err != sdk.ErrAdmission {
		t.Fatal("missing store accepted", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created missing database")
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Add(-time.Hour)
	f := sdk.MemoryFact{Version: 1, ID: "fact", Scope: "project", Revision: 1, Content: "private-secret preference", Provenance: "operator", Confidence: 1, Privacy: "local_only", Created: now, Updated: now, Expires: now.Add(time.Minute)}
	if err = db.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	f.Scope = "foreign"
	if err = db.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	got, err := c.ExportMemory(ctx)
	if err != nil || got.Scope != "project" || len(got.Facts) != 1 || got.Facts[0].Content != "[REDACTED] preference" || got.Facts[0].Expires.IsZero() {
		t.Fatal(got, err)
	}
	raw, err := db.GetMemory(ctx, "project", "fact")
	if err != nil || raw.Content != "private-secret preference" || !raw.LastUse.IsZero() {
		t.Fatal("export mutated source", err)
	}
	for _, client := range []*sdk.Client{nil, {}} {
		if _, err := client.ExportMemory(ctx); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
	}
	if _, err := c.ExportMemory(nil); err != sdk.ErrAdmission {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ExportMemory(canceled); err != sdk.ErrAdmission {
		t.Fatal(err)
	}
}
