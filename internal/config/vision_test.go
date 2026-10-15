package config

import "testing"

func TestVisionRequiresSupportedImageTransport(t *testing.T) {
	s := codexProviderSettings()
	s.Models[1].Capabilities = append(s.Models[1].Capabilities, "vision")
	if e := s.Validate(); e != nil {
		t.Fatal("Ollama vision rejected", e)
	}
	s.Models[0].Capabilities = append(s.Models[0].Capabilities, "vision")
	if s.Validate() == nil {
		t.Fatal("vision advertised on an unsupported transport")
	}
	s.Models[0].Capabilities = []string{"chat"}
	s.Providers[1].Kind = "openai_compatible"
	if e := s.Validate(); e != nil {
		t.Fatal("OpenAI-compatible vision rejected", e)
	}
}
