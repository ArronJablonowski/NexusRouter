package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// Exercise built-in scope selection, actual application dispatch and the same
// durable lease boundary used by readers. No user files or live inference.
func TestBuiltinCreateExcludesOverlappingFilesystemReaders(t *testing.T) {
	for _, layout := range []string{"same", "nested", "alias", "unrelated"} {
		t.Run(layout, func(t *testing.T) {
			cfg, calls, _ := createIntegrationConfig(t, `{"path":"result.txt","content":"must wait"}`)
			switch layout {
			case "nested":
				cfg.Tools.CreateRoot = filepath.Join(cfg.Tools.ReadRoot, "child")
				if err := os.Mkdir(cfg.Tools.CreateRoot, 0700); err != nil {
					t.Fatal(err)
				}
			case "alias":
				cfg.Tools.CreateRoot = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(cfg.Tools.ReadRoot, cfg.Tools.CreateRoot); err != nil {
					t.Fatal(err)
				}
			case "unrelated":
				cfg.Tools.CreateRoot = t.TempDir()
			}
			registry, closeRead, err := readTools(cfg.Tools.ReadRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer closeRead()
			readScope := registry.Descriptions()[0].Scope
			closeCreate, writeScope, err := registerCreateTool(registry, cfg.Tools.CreateRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer closeCreate()
			if readScope != "workspace" || readScope != writeScope {
				t.Fatal("file scope mismatch")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			e := runtime.Event{Version: 1, ID: "reader-start", TaskID: "reader", SessionID: "reader", CorrelationID: "reader", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
			if err = db.Append(ctx, 0, e); err != nil {
				t.Fatal(err)
			}
			lease, err := db.AcquireLease(ctx, "reader", "fixture-holder", readScope, false, time.Now().UTC(), 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer db.ReleaseLease(context.Background(), lease.Token, lease.Owner)
			reviews := 0
			svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, nil, nil, func(ctx context.Context, p tools.ApprovalPrompt) (string, bool, error) {
				reviews++
				if p.Request.Scope != readScope {
					t.Error("approval uses different scope")
				}
				return "fixture-operator", true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := svc.Run(ctx, Request{ModelID: "m", Prompt: "create fixture"})
			if runErr == nil || reviews != 1 || calls.Load() != 1 {
				t.Fatal("writer not denied at lease boundary", runErr, reviews, calls.Load())
			}
			if _, err = os.Stat(filepath.Join(cfg.Tools.CreateRoot, "result.txt")); !os.IsNotExist(err) {
				t.Fatal("writer ran during reader ownership", err)
			}
			leases, err := db.InspectLeases(ctx, readScope)
			if err != nil || len(leases) != 1 || leases[0].Token != lease.Token {
				t.Fatal("reader ownership changed", err)
			}
		})
	}
}
