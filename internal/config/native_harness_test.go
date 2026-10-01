package config

import (
	"strings"
	"testing"
)

func TestNativeHarnessConfigValidation(t *testing.T) {
	fixture := func() Settings {
		s := Defaults()
		s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
		s.Models = []Model{{ID: "chat", Provider: "local"}}
		s.NativeHarnesses = []NativeHarness{{ID: "pi", Kind: "pi", ModelID: "chat", Executable: "/opt/pi", ExecutableSHA256: strings.Repeat("a", 64), ModelRevision: "r1", MaxOutputTokens: 1024, OverheadRAMBytes: 1, Prices: &NativeHarnessPrices{}}}
		return s
	}
	s := fixture()
	if s.validateNativeHarnesses() != nil {
		t.Fatal("valid registration rejected")
	}
	for _, change := range []func(*Settings){
		func(s *Settings) { s.NativeHarnesses[0].Executable = "relative" },
		func(s *Settings) { s.NativeHarnesses[0].Prices = nil },
		func(s *Settings) { s.NativeHarnesses[0].ExecutableSHA256 = "unpinned" },
		func(s *Settings) { s.NativeHarnesses[0].Kind = "unknown" },
		func(s *Settings) { s.NativeHarnesses[0].Kind = "openhands" },
		func(s *Settings) { s.NativeHarnesses[0].ID = "auto" },
		func(s *Settings) { s.NativeHarnesses[0].ModelID = "missing" },
		func(s *Settings) { s.NativeHarnesses = append(s.NativeHarnesses, s.NativeHarnesses[0]) },
	} {
		s := fixture()
		change(&s)
		if s.validateNativeHarnesses() == nil {
			t.Fatal("invalid registration accepted")
		}
	}
}

func TestNativeHarnessEvidencePathAndRedaction(t *testing.T) {
	s := Defaults()
	for _, path := range []string{"relative", "/tmp/../evidence", "/tmp/evidence\n"} {
		s.NativeHarnessEvidenceDir = path
		if s.validateNativeHarnesses() == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	s.NativeHarnessEvidenceDir = "/private/operator/evidence"
	s.NativeHarnesses = []NativeHarness{{Executable: "/private/operator/pi", HermesSourceDir: "/private/operator/hermes"}}
	data, err := s.RedactedJSON()
	if err != nil || strings.Contains(string(data), "/private/operator") {
		t.Fatal("path exposure", err)
	}
	if s.NativeHarnesses[0].Executable != "/private/operator/pi" || s.NativeHarnesses[0].HermesSourceDir != "/private/operator/hermes" || s.NativeHarnessEvidenceDir != "/private/operator/evidence" {
		t.Fatal("inspection mutated settings")
	}
	s.NativeHarnesses = nil
	if s.validateNativeHarnesses() != nil {
		t.Fatal("absolute clean evidence path rejected")
	}
}
