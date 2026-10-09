package config

import (
	"strings"
	"testing"
)

func TestProviderRunnerMetadataValidation(t *testing.T) {
	for _, runner := range []string{"MLX-LM", "llama.cpp", "Ollama"} {
		cfg := Defaults()
		cfg.Providers = []Provider{{ID: "local", Endpoint: "http://127.0.0.1:8080", Kind: "openai_compatible", Runner: runner}}
		if cfg.Validate() != nil {
			t.Fatal(runner, cfg.Validate())
		}
	}
	for _, runner := range []string{" x", "x\ny", strings.Repeat("x", 129)} {
		cfg := Defaults()
		cfg.Providers = []Provider{{ID: "local", Endpoint: "http://127.0.0.1:8080", Kind: "ollama", Runner: runner}}
		if cfg.Validate() == nil {
			t.Fatal("invalid runner accepted", runner)
		}
	}
}
