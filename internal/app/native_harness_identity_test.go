package app

import (
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"strings"
	"testing"
)

func TestNativeHarnessIdentityPreviewContextAndNoEffects(t *testing.T) {
	for _, kind := range []string{"pi", "openclaw", "goose", "openhands", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Tools.Enabled = true
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1", RequestTimeout: "15s"}}
			cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 32768, Capabilities: []string{"chat"}}}
			cfg.NativeHarnesses = []config.NativeHarness{{ID: "pair", Kind: kind, NativeTools: true, ModelID: "chat", Executable: "/missing/harness", ExecutableSHA256: strings.Repeat("a", 64), RuntimeSHA256: strings.Repeat("b", 64), HermesSourceDir: "/missing/hermes", ModelRevision: "v1", MaxOutputTokens: 1024, OverheadRAMBytes: 1, Prices: &config.NativeHarnessPrices{}}}
			s, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.secret = func(string) string { t.Fatal("preview read secret"); return "" }
			first, err := s.NativeHarnessIdentity("chat", "pair", cfg.Models[0].WorkingContextTokens())
			if err != nil || first != s.nativeHarnessIdentities["pair"] {
				t.Fatal(first, err)
			}
			smaller, err := s.NativeHarnessIdentity("chat", "pair", 16384)
			if err != nil {
				t.Fatal(err)
			}
			larger, err := s.NativeHarnessIdentity("chat", "pair", 32768)
			if err != nil || smaller == larger {
				t.Fatal("context identity borrowed", err)
			}
			r := Request{ModelID: "chat", HarnessID: "pair", ContextTokens: 16384, ExpectedHarnessIdentity: &smaller, Prompt: "fixture", Domain: "writing", Profile: "identity-v1"}
			bound, e := s.bindNativeHarness(r)
			if e != nil || bound.ExpectedHarnessIdentity == r.ExpectedHarnessIdentity {
				t.Fatal("pin not copied", e)
			}
			r.ExpectedHarnessIdentity = &larger
			if _, e = s.bindNativeHarness(r); e == nil {
				t.Fatal("changed context identity accepted")
			}
			r.ExpectedHarnessIdentity = &smaller
			r.HarnessID = "auto"
			if _, e = s.bindNativeHarness(r); e == nil {
				t.Fatal("auto silently ignored identity pin")
			}
			for _, q := range []struct {
				m, h string
				n    int
			}{{"other", "pair", 16384}, {"chat", "auto", 16384}, {"chat", "pair", 32769}, {"chat", "pair", 1}} {
				if _, err := s.NativeHarnessIdentity(q.m, q.h, q.n); err == nil {
					t.Fatal("invalid preview", q)
				}
			}
		})
	}
}
