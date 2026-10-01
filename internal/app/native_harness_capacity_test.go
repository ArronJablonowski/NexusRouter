package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestNativeHarnessCapacityUsesContextOverheadAndLiveReservations(t *testing.T) {
	cfg := config.Defaults()
	cfg.Hardware.Concurrent = "1"
	cfg.Tools.Enabled = false
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "unused.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 400, DefaultContextTokens: 8192, ContextTokens: 32768, Capabilities: []string{"chat"}}}
	cfg.NativeHarnesses = []config.NativeHarness{{ID: "pair", Kind: "pi", ModelID: "chat", Executable: "/missing/pi", ExecutableSHA256: strings.Repeat("a", 64), ModelRevision: "v1", MaxOutputTokens: 1024, OverheadRAMBytes: 100, Prices: &config.NativeHarnessPrices{}}}
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.profile = func(context.Context) (resources.Snapshot, error) { calls++; return resourcePlanSnapshot(), nil }
	s.secret = func(string) string { t.Fatal("read secret"); return "" }
	id, need, first, err := s.NativeHarnessCapacity(context.Background(), "chat", "pair", 8192)
	if err != nil || need.RAM != 500 || first.Action != resources.CapacityAdmit || calls != 1 {
		t.Fatal(id, need, first, err, calls)
	}
	_, larger, wait, err := s.NativeHarnessCapacity(context.Background(), "chat", "pair", 32768)
	if err != nil || larger.RAM != 1700 || wait.Action != resources.CapacityWait {
		t.Fatal(larger, wait, err)
	}
	release, err := s.budget.Reserve(resourcePlanSnapshot(), need, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, _, held, err := s.NativeHarnessCapacity(context.Background(), "chat", "pair", 8192)
	release()
	if err != nil || held.Action != resources.CapacityWait {
		t.Fatal("ignored live reservation", held, err)
	}
	_, _, again, err := s.NativeHarnessCapacity(context.Background(), "chat", "pair", 8192)
	if err != nil || again.Action != resources.CapacityAdmit {
		t.Fatal(again, err)
	}
	before := calls
	if _, _, _, err = s.NativeHarnessCapacity(context.Background(), "other", "pair", 8192); err == nil || calls != before {
		t.Fatal("invalid request measured", err)
	}
	// Cloud inference still reserves local harness overhead, not remote weights.
	cfg.Mode = "cloud_only"
	cfg.Models[0].Locality = "cloud"
	cloud, e := NewService(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	cloud.profile = func(context.Context) (resources.Snapshot, error) { return resourcePlanSnapshot(), nil }
	_, cloudNeed, cloudCapacity, e := cloud.NativeHarnessCapacity(context.Background(), "chat", "pair", 32768)
	if e != nil || cloudNeed.RAM != 100 || cloudNeed.VRAM != 0 || cloudCapacity.Action != resources.CapacityAdmit {
		t.Fatal(cloudNeed, cloudCapacity, e)
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("capacity wrote runtime store", err)
	}
}
