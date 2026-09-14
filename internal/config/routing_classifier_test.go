package config

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/classification"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestRoutingClassifierDefaultsLayersAndFingerprint(t *testing.T) {
	defaults := Defaults()
	want := RoutingClassifier{MaxInputTokens: 4096, MaxOutputTokens: 256, Timeout: "30s"}
	if defaults.Routing.Classifier != want {
		t.Fatalf("unexpected classifier defaults: %+v", defaults.Routing.Classifier)
	}

	options := Options{
		ProjectFile: file(t, "routing:\n  classifier:\n    max_cost: 1\n    max_input_tokens: 2048\n"),
		Env: map[string]string{
			"routing.classifier.max_output_tokens": "128",
			"routing.classifier.timeout":           "45s",
		},
		Flags: map[string]string{"routing.classifier.max_cost": "0.25"},
	}
	settings, err := Load(options)
	if err != nil {
		for name, narrowed := range map[string]Options{
			"project": {ProjectFile: options.ProjectFile},
			"env":     {Env: options.Env},
			"flags":   {Flags: options.Flags},
		} {
			if _, narrowErr := Load(narrowed); narrowErr != nil {
				t.Fatalf("%s classifier layer rejected: %v", name, narrowErr)
			}
		}
		t.Fatal(err)
	}
	got := settings.Routing.Classifier
	if got.Enabled || got.ModelID != "" || got.MaxCost != .25 || got.MaxInputTokens != 2048 || got.MaxOutputTokens != 128 || got.Timeout != "45s" {
		t.Fatalf("classifier layers decoded incorrectly: %+v", got)
	}

	display, err := settings.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(display, []byte(`"classifier"`)) || !bytes.Contains(display, []byte(`"max_input_tokens": 2048`)) ||
		!bytes.Contains(display, []byte(`"max_output_tokens": 128`)) || !bytes.Contains(display, []byte(`"timeout": "45s"`)) {
		t.Fatalf("classifier absent from redacted configuration fingerprint: %s", display)
	}
	if settings.Routing.Classifier != got {
		t.Fatal("redacted display mutated classifier settings")
	}
	body, err := json.Marshal(defaults.Routing.Classifier)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"enabled":false,"model_id":"","max_cost":0,"max_input_tokens":4096,"max_output_tokens":256,"timeout":"30s"}` {
		t.Fatalf("disabled classifier fingerprint changed: %s", body)
	}
}

func TestEnabledRoutingClassifierRequiresBoundedAvailableModel(t *testing.T) {
	s := validRoutingClassifierSettings("local", 0.1)
	if err := s.Validate(); err != nil {
		t.Fatalf("valid classifier rejected: %v", err)
	}

	tests := map[string]func(*Settings){
		"missing alias":        func(s *Settings) { s.Routing.Classifier.ModelID = "" },
		"unknown alias":        func(s *Settings) { s.Routing.Classifier.ModelID = "missing" },
		"invalid alias":        func(s *Settings) { s.Routing.Classifier.ModelID = "not valid" },
		"missing context":      func(s *Settings) { s.Models[0].ContextTokens = 0 },
		"missing estimate":     func(s *Settings) { s.Models[0].EstimatedCost = nil },
		"negative estimate":    func(s *Settings) { *s.Models[0].EstimatedCost = -1 },
		"nonfinite estimate":   func(s *Settings) { *s.Models[0].EstimatedCost = math.Inf(1) },
		"estimate over budget": func(s *Settings) { *s.Models[0].EstimatedCost = .11 },
		"negative input":       func(s *Settings) { s.Routing.Classifier.MaxInputTokens = -1 },
		"negative output":      func(s *Settings) { s.Routing.Classifier.MaxOutputTokens = -1 },
		"excess output": func(s *Settings) {
			s.Routing.Classifier.MaxOutputTokens = providers.MaxOutputTokens + 1
		},
		"zero input":         func(s *Settings) { s.Routing.Classifier.MaxInputTokens = 0 },
		"zero output":        func(s *Settings) { s.Routing.Classifier.MaxOutputTokens = 0 },
		"input over context": func(s *Settings) { s.Routing.Classifier.MaxInputTokens = 8193 },
		"combined overflow": func(s *Settings) {
			s.Routing.Classifier.MaxInputTokens = 8000
			s.Routing.Classifier.MaxOutputTokens = 193
		},
		"short timeout":   func(s *Settings) { s.Routing.Classifier.Timeout = "99ms" },
		"long timeout":    func(s *Settings) { s.Routing.Classifier.Timeout = "1m1ns" },
		"invalid timeout": func(s *Settings) { s.Routing.Classifier.Timeout = "later" },
		"negative cost":   func(s *Settings) { s.Routing.Classifier.MaxCost = -1 },
		"nonfinite cost":  func(s *Settings) { s.Routing.Classifier.MaxCost = math.NaN() },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := validRoutingClassifierSettings("local", .1)
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("unsafe classifier configuration accepted")
			}
		})
	}
}

func TestEnabledRoutingClassifierLoadsFromYAML(t *testing.T) {
	body := `providers:
  - id: local
    kind: ollama
models:
  - id: intent
    provider: local
    model: intent-native
    locality: local
    capabilities: [chat]
    context_tokens: 8192
    estimated_cost: 0
routing:
  classifier:
    enabled: true
    model_id: intent
    max_cost: 0
    max_input_tokens: 4096
    max_output_tokens: 256
    timeout: 30s
`
	s, err := Load(Options{ProjectFile: file(t, body)})
	if err != nil {
		t.Fatalf("valid classifier YAML rejected: %v", err)
	}
	if classifier := s.Routing.Classifier; !classifier.Enabled || classifier.ModelID != "intent" || classifier.MaxInputTokens != 4096 || classifier.MaxOutputTokens != 256 {
		t.Fatalf("classifier YAML decoded incorrectly: %+v", classifier)
	}
}

func TestRoutingClassifierModeAndDisabledBinding(t *testing.T) {
	for _, test := range []struct {
		mode, locality string
		valid          bool
	}{
		{"local_only", "local", true}, {"local_only", "cloud", false},
		{"cloud_only", "cloud", true}, {"cloud_only", "local", false},
		{"hybrid", "local", true}, {"hybrid", "cloud", true},
	} {
		s := validRoutingClassifierSettings(test.locality, 0)
		s.Mode = test.mode
		if got := s.Validate() == nil; got != test.valid {
			t.Fatalf("mode=%s locality=%s validity=%v want %v", test.mode, test.locality, got, test.valid)
		}
	}
	s := Defaults()
	s.Routing.Classifier.ModelID = "latent-binding"
	if err := s.Validate(); err == nil {
		t.Fatal("disabled classifier retained a model binding")
	}
}

func TestRoutingClassifierBoundariesAndStrictSchema(t *testing.T) {
	for _, test := range []struct {
		timeout string
		input   int64
		output  int64
	}{
		{"100ms", 1, 1},
		{"1m", 4096, 256},
		{"30s", 1, classification.MaxClassifierOutputTokens},
	} {
		s := validRoutingClassifierSettings("local", 0)
		if test.output == classification.MaxClassifierOutputTokens {
			s.Models[0].ContextTokens = int(classification.MaxClassifierOutputTokens + 1)
		}
		s.Routing.Classifier.Timeout, s.Routing.Classifier.MaxInputTokens, s.Routing.Classifier.MaxOutputTokens = test.timeout, test.input, test.output
		if err := s.Validate(); err != nil {
			t.Fatalf("valid classifier boundary rejected: %+v: %v", test, err)
		}
	}
	for _, body := range []string{
		"routing:\n  classifier:\n    unknown: true\n",
		"routing:\n  classifier:\n    enabled: yes\n",
		"routing:\n  classifier:\n    max_cost: '1'\n",
		"routing:\n  classifier:\n    max_input_tokens: 1.5\n",
		"routing:\n  classifier:\n    max_output_tokens: '256'\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("invalid classifier schema accepted: %q", body)
		}
	}
	for _, override := range []map[string]string{
		{"routing.classifier.max_input_tokens": "1.5"},
		{"routing.classifier.max_output_tokens": "secret-value"},
	} {
		if _, err := Load(Options{Env: override}); err == nil {
			t.Fatalf("invalid classifier scalar override accepted: %#v", override)
		}
	}
	_, err := Load(Options{
		Env:   map[string]string{"routing.classifier.max_cost": "secret-value"},
		Flags: map[string]string{"routing.classifier.max_cost": "0.1"},
	})
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("shadowed classifier override accepted or leaked: %v", err)
	}
}

func validRoutingClassifierSettings(locality string, cost float64) Settings {
	s := Defaults()
	providerKind, endpoint := "ollama", DefaultOllamaEndpoint
	if locality == "cloud" {
		providerKind, endpoint = "openai_compatible", "https://example.invalid/v1"
	}
	s.Providers = []Provider{{ID: "classifier-provider", Kind: providerKind, Endpoint: endpoint}}
	s.Models = []Model{{
		ID: "classifier", Provider: "classifier-provider", Model: "native-classifier", Locality: locality,
		Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &cost,
	}}
	s.Routing.Classifier.Enabled = true
	s.Routing.Classifier.ModelID = "classifier"
	s.Routing.Classifier.MaxCost = .1
	return s
}
