package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestNativeSubmissionFingerprintIncludesAdapterContract(t *testing.T) {
	cfg := config.Defaults()
	cfg.Tools.Enabled = false
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434", RequestTimeout: "15s"}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}}}
	cfg.NativeHarnesses = []config.NativeHarness{{ID: "pi-local", Kind: "pi", ModelID: "chat", Executable: "/operator/pi", ExecutableSHA256: strings.Repeat("a", 64), ModelRevision: "v1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &config.NativeHarnessPrices{}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	first, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := first.submissionConfigDigest()
	if before != restarted.submissionConfigDigest() {
		t.Fatal("equivalent startup changed durable authority")
	}
	identity := restarted.nativeHarnessIdentities["pi-local"]
	identity.AdapterVersion += "-changed"
	restarted.nativeHarnessIdentities["pi-local"] = identity
	if before == restarted.submissionConfigDigest() {
		t.Fatal("changed compiled adapter contract did not fence queue")
	}
	_, _, _, err = first.submissionPayload("native-unknown-key", Request{ModelID: "chat", HarnessID: "unknown", Prompt: "fixture"})
	if !errors.Is(err, ErrHarnessUnsupported) || !errors.Is(err, ErrAdmission) {
		t.Fatal("unknown queued registration admitted", err)
	}
}
