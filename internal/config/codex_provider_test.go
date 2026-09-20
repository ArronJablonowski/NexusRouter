package config

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func codexProviderSettings() Settings {
	s := Defaults()
	s.Providers = []Provider{{ID: "brain", Kind: "codex_app_server", Executable: "/private/bin/codex"}, {ID: "worker", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	s.Models = []Model{{ID: "coordinator", Provider: "brain", Model: "gpt-5.6-sol", Locality: "cloud", ContextTokens: 4096, Capabilities: []string{"chat"}}, {ID: "local", Provider: "worker", Model: "local-model", Locality: "local", Capabilities: []string{"chat"}}}
	return s
}

func TestCodexProviderConfiguration(t *testing.T) {
	for _, mode := range []string{"hybrid", "cloud_only", "local_only"} {
		s := codexProviderSettings()
		s.Mode = mode
		if err := s.Validate(); err != nil {
			t.Fatalf("configured cloud coordinator rejected for %s: %v", mode, err)
		}
	}
	s := codexProviderSettings()
	redacted, err := s.RedactedJSON()
	if err != nil || strings.Contains(string(redacted), s.Providers[0].Executable) || s.Providers[0].Executable != "/private/bin/codex" {
		t.Fatal("executable redaction changed source or exposed path")
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		raw, err := marshal(s.Providers[0])
		if err != nil || !strings.Contains(string(raw), "executable") {
			t.Fatal("executable field missing from serialization")
		}
	}
}

func TestCodexCoordinatorReasoningEffort(t *testing.T) {
	s := codexProviderSettings()
	s.Models[0].ReasoningEffort = "medium"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Models[0].ReasoningEffort = "extreme"
	if s.Validate() == nil {
		t.Fatal("unknown reasoning effort accepted")
	}
	s = codexProviderSettings()
	s.Models[1].ReasoningEffort = "medium"
	if s.Validate() == nil {
		t.Fatal("reasoning effort accepted for unsupported provider")
	}
}

func TestSolCodexLocalSmokeSelectsMediumSolCommander(t *testing.T) {
	s, err := Load(Options{ProjectFile: "../../examples/sol-codex-local-smoke.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if s.WebUI.DefaultModel != "coordinator" || s.WebUI.CommanderFallbackModel != "muse-glimmer" || len(s.Models) != 3 || s.Models[0].Model != "gpt-5.6-sol" || s.Models[0].ReasoningEffort != "medium" || s.Models[2].Model != "muse-glimmer:30b-mlx" || s.Models[2].Locality != "local" {
		t.Fatal("Sol commander selection changed", s.WebUI.DefaultModel, s.Models)
	}
}

func TestCodexProviderConfigurationRejectsInvalid(t *testing.T) {
	cases := map[string]func(*Settings){
		"missing executable":      func(s *Settings) { s.Providers[0].Executable = "" },
		"relative executable":     func(s *Settings) { s.Providers[0].Executable = "codex" },
		"oversized executable":    func(s *Settings) { s.Providers[0].Executable = "/" + strings.Repeat("a", 4096) },
		"nul executable":          func(s *Settings) { s.Providers[0].Executable = "/bin/\x00codex" },
		"invalid utf8 executable": func(s *Settings) { s.Providers[0].Executable = "/bin/\xffcodex" },
		"endpoint":                func(s *Settings) { s.Providers[0].Endpoint = "http://localhost" },
		"API key":                 func(s *Settings) { s.Providers[0].APIKeyEnv = "OPENAI_API_KEY" },
		"HTTP executable":         func(s *Settings) { s.Providers[1].Executable = "/bin/codex" },
		"OpenAI executable":       func(s *Settings) { s.Providers[1].Kind = "openai_compatible"; s.Providers[1].Executable = "/bin/codex" },
		"local model":             func(s *Settings) { s.Models[0].Locality = "local" },
		"different model":         func(s *Settings) { s.Models[0].Model = "other" },
		"missing context":         func(s *Settings) { s.Models[0].ContextTokens = 0 },
		"negative context":        func(s *Settings) { s.Models[0].ContextTokens = -1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := codexProviderSettings()
			change(&s)
			if s.Validate() == nil {
				t.Fatal("invalid Codex configuration admitted")
			}
		})
	}
}
