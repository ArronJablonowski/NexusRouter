package config

import (
	"strconv"
	"strings"
	"testing"
)

func TestModelCatalogMetadataIsBoundedAtConfiguration(t *testing.T) {
	base := Defaults()
	base.Providers = []Provider{{ID: "local", Kind: "ollama"}}
	base.Models = []Model{{ID: "model", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}}}
	if base.Validate() != nil {
		t.Fatal("valid fixture rejected")
	}
	for _, mutate := range []func(*Model){
		func(m *Model) { m.Model = " fixture" },
		func(m *Model) { m.Model = "fixture\nname" },
		func(m *Model) { m.Model = strings.Repeat("x", 513) },
		func(m *Model) { m.Capabilities = []string{"chat", "chat"} },
		func(m *Model) {
			m.Capabilities = make([]string, 129)
			for i := range m.Capabilities {
				m.Capabilities[i] = "c" + strings.Repeat("x", i)
			}
		},
	} {
		settings := base
		settings.Models = append([]Model(nil), base.Models...)
		mutate(&settings.Models[0])
		if settings.Validate() == nil {
			t.Fatal("unsafe catalog metadata accepted")
		}
	}
}

func TestConfiguredModelCountBoundary(t *testing.T) {
	settings := Defaults()
	settings.Providers = []Provider{{ID: "local", Kind: "ollama"}}
	settings.Models = make([]Model, 256)
	for i := range settings.Models {
		suffix := strconv.Itoa(i)
		settings.Models[i] = Model{ID: "model-" + suffix, Provider: "local", Model: "fixture-" + suffix, Locality: "local", Capabilities: []string{"chat"}}
	}
	if err := settings.Validate(); err != nil {
		t.Fatal("256 configured models rejected", err)
	}
	settings.Models = append(settings.Models, Model{ID: "model-256", Provider: "local", Model: "fixture-256", Locality: "local", Capabilities: []string{"chat"}})
	if settings.Validate() == nil {
		t.Fatal("257 configured models accepted")
	}
}
