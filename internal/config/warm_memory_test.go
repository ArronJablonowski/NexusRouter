package config

import (
	"strings"
	"testing"
)

func TestDedicatedWarmMemoryConfiguration(t *testing.T) {
	base := func() Settings {
		s := residencySettings()
		s.Providers[0].ManageResidency = false
		s.Providers[0].DedicatedWarmMemory = true
		s.Hardware.Concurrent = "1"
		s.Workers.Max = 1
		s.Models[0].RAMBytes = 64 << 30
		s.Models[0].WarmRAMBytes = 32 << 30
		s.Models[0].ResidencyDigest = strings.Repeat("a", 64)
		s.Models[0].ContextTokens = 8192
		return s
	}
	if err := base().Validate(); err != nil {
		t.Fatal(err)
	}
	parallel := base()
	parallel.Hardware.Concurrent = "auto"
	parallel.Workers.Max = 4
	if err := parallel.Validate(); err != nil {
		t.Fatal("parallel cold fallback rejected", err)
	}
	for name, mutate := range map[string]func(*Settings){
		"shared":      func(s *Settings) { s.Providers[0].DedicatedWarmMemory = false },
		"remote":      func(s *Settings) { s.Providers[0].Endpoint = "http://10.77.7.202:11434" },
		"unload":      func(s *Settings) { s.Providers[0].ManageResidency = true },
		"digest":      func(s *Settings) { s.Models[0].ResidencyDigest = "bad" },
		"budget":      func(s *Settings) { s.Models[0].WarmRAMBytes = 1 << 30 },
		"vram":        func(s *Settings) { s.Models[0].VRAMBytes = 1 },
		"other model": func(s *Settings) { m := s.Models[0]; m.ID = "other"; m.Model = "other"; s.Models = append(s.Models, m) },
	} {
		t.Run(name, func(t *testing.T) {
			s := base()
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("unsafe warm configuration accepted")
			}
		})
	}
}
