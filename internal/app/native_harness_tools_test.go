package app

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/harness/goose"
	"github.com/ArronJablonowski/NexusRouter/harness/hermes"
	"github.com/ArronJablonowski/NexusRouter/harness/openclaw"
	"github.com/ArronJablonowski/NexusRouter/harness/openhands"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"strings"
	"testing"
)

func TestNativeToolsIdentityAndConstructorSnapshot(t *testing.T) {
	for _, kind := range []string{"pi", "openhands", "goose", "hermes", "openclaw"} {
		t.Run(kind, func(t *testing.T) { testNativeToolsSnapshot(t, kind) })
	}
}
func testNativeToolsSnapshot(t *testing.T, kind string) {
	adapter := pi.AgentAdapterVersion
	if kind == "openclaw" {
		adapter = openclaw.AgentAdapterVersion
	}
	if kind == "hermes" {
		adapter = hermes.AgentAdapterVersion
	}
	if kind == "goose" {
		adapter = goose.AgentAdapterVersion
	}
	if kind == "openhands" {
		adapter = openhands.AgentAdapterVersion
	}

	cfg := config.Defaults()
	cfg.Tools.Enabled = false
	cfg.Tools.CollaborationEnabled = true
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434", RequestTimeout: "15s"}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}}}
	cfg.NativeHarnesses = []config.NativeHarness{{ID: "pi-local", NativeTools: true, Kind: kind, HermesSourceDir: "/operator/hermes", RuntimeSHA256: strings.Repeat("b", 64), ModelID: "chat", Executable: "/operator/pi", ExecutableSHA256: strings.Repeat("a", 64), ModelRevision: "v1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &config.NativeHarnessPrices{}}}
	extension := func(scope string) *tools.Extension {
		e, err := tools.NewExtension([]tools.Definition{{Tool: providers.Tool{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}, Scope: scope, ReadOnly: true, Behavior: runtime.BehaviorReadOnly, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			t.Fatal("registration invoked tool")
			return runtime.ToolResult{}, nil
		}}}, &tools.Policy{Default: tools.Allow})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	first, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, nil, extension("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, nil, extension("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first.nativeHarnessIdentities["pi-local"].AdapterVersion != adapter || first.submissionConfigDigest() == second.submissionConfigDigest() {
		t.Fatal("tool scope not bound to attribution and queued authority")
	}
	before := first.submissionConfigDigest()
	cfg.Models[0].Model = "mutated"
	cfg.NativeHarnesses[0].Prices.Input = 99
	if first.submissionConfigDigest() != before {
		t.Fatal("caller changed service snapshot")
	}
	if _, err := first.bindNativeHarness(Request{HarnessID: "pi-local", ModelID: "chat"}); err != nil {
		t.Fatal(err)
	}
	entry := first.nativeHarnesses["pi-local"]
	entry.NativeTools = false
	first.nativeHarnesses["pi-local"] = entry
	if _, err := first.bindNativeHarness(Request{HarnessID: "pi-local", ModelID: "chat"}); err == nil {
		t.Fatal("legacy adapter gained host tools")
	}
	entry.NativeTools = true
	first.nativeHarnesses["pi-local"] = entry
	first.settings.Models[0].Locality = "cloud"
	if _, err := first.bindNativeHarness(Request{HarnessID: "pi-local", ModelID: "chat"}); err == nil {
		t.Fatal("cloud host tools admitted")
	}
}
