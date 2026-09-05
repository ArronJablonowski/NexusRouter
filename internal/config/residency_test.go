package config

import (
	"strings"
	"testing"
)

func residencySettings() Settings {
	s := Defaults()
	s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434", ManageResidency: true}}
	s.Models = []Model{{ID: "small", Provider: "local", Model: "small:latest", Locality: "local", Capabilities: []string{"chat"}}}
	return s
}

func TestManagedResidencyConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:11434", "http://localhost:11434/", "https://[::1]:11434"} {
		s := residencySettings()
		s.Providers[0].Endpoint = endpoint
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		b, err := s.RedactedJSON()
		if err != nil || !strings.Contains(string(b), `"manage_residency": true`) || strings.Contains(string(b), endpoint) {
			t.Fatal("residency authority or endpoint redaction lost")
		}
	}
	input := "providers:\n  - id: local\n    kind: ollama\n    endpoint: http://localhost:11434\n    manage_residency: true\n"
	s, err := Load(Options{ProjectFile: file(t, input)})
	if err != nil || !s.Providers[0].ManageResidency {
		t.Fatal("explicit opt-in failed", err)
	}
	s, err = Load(Options{ProjectFile: file(t, strings.ReplaceAll(input, "    manage_residency: true\n", ""))})
	if err != nil || s.Providers[0].ManageResidency {
		t.Fatal("implicit residency authority", err)
	}
}

func TestManagedResidencyRejectsAmbiguousAuthority(t *testing.T) {
	cases := map[string]func(*Settings){
		"cloud model":   func(s *Settings) { s.Models[0].Locality = "cloud" },
		"other adapter": func(s *Settings) { s.Providers[0].Kind = "openai_compatible" },
		"nonloopback":   func(s *Settings) { s.Providers[0].Endpoint = "https://example.com" },
		"path":          func(s *Settings) { s.Providers[0].Endpoint += "/private" },
		"query":         func(s *Settings) { s.Providers[0].Endpoint += "?" },
		"encoded path":  func(s *Settings) { s.Providers[0].Endpoint += "/%2F" },
		"zero port":     func(s *Settings) { s.Providers[0].Endpoint = "http://localhost:0" },
		"alias model": func(s *Settings) {
			m := s.Models[0]
			m.ID, m.Model = "duplicate", "small"
			s.Models = append(s.Models, m)
		},
		"alias endpoint": func(s *Settings) {
			s.Providers = append(s.Providers, Provider{ID: "other", Kind: "openai_compatible", Endpoint: "http://localhost:11434/v1"})
		},
		"ipv6 alias endpoint": func(s *Settings) {
			s.Providers = append(s.Providers, Provider{ID: "other", Kind: "ollama", Endpoint: "https://[::1]:11434/"})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := residencySettings()
			change(&s)
			if s.Validate() == nil {
				t.Fatal("ambiguous residency authority admitted")
			}
		})
	}
}
