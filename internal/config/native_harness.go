package config

import (
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"unicode"
)

// NativeHarness declares an operator-trusted installed runtime, never credentials.
type NativeHarness struct {
	NativeTools      bool                 `yaml:"native_tools,omitempty" json:"native_tools,omitempty"`
	ID               string               `yaml:"id" json:"id"`
	Kind             string               `yaml:"kind" json:"kind"`
	ModelID          string               `yaml:"model_id" json:"model_id"`
	Executable       string               `yaml:"executable" json:"executable"`
	ExecutableSHA256 string               `yaml:"executable_sha256" json:"executable_sha256"`
	ModelRevision    string               `yaml:"model_revision" json:"model_revision"`
	RuntimeSHA256    string               `yaml:"runtime_sha256,omitempty" json:"runtime_sha256,omitempty"`
	HermesSourceDir  string               `yaml:"hermes_source_dir,omitempty" json:"hermes_source_dir,omitempty"`
	MaxOutputTokens  int                  `yaml:"max_output_tokens" json:"max_output_tokens"`
	OverheadRAMBytes uint64               `yaml:"overhead_ram_bytes" json:"overhead_ram_bytes"`
	Prices           *NativeHarnessPrices `yaml:"prices" json:"prices"`
}
type NativeHarnessPrices struct {
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache_read" json:"cache_read"`
	CacheWrite float64 `yaml:"cache_write" json:"cache_write"`
}

func (s Settings) validateNativeHarnesses() error {
	bad := errors.New("invalid native harness configuration")
	if d := s.NativeHarnessEvidenceDir; d != "" && (!filepath.IsAbs(d) || filepath.Clean(d) != d || strings.ContainsFunc(d, unicode.IsControl)) {
		return bad
	}
	if len(s.NativeHarnesses) > 256 {
		return bad
	}
	seen := map[string]bool{}
	digest := func(v string) bool {
		b, e := hex.DecodeString(v)
		return e == nil && len(b) == 32 && strings.ToLower(v) == v
	}
	for _, h := range s.NativeHarnesses {
		if (h.NativeTools && h.Kind != "pi" && h.Kind != "openhands") || h.ID == "" || h.ID == "auto" || len(h.ID) > 128 || strings.TrimSpace(h.ID) != h.ID || strings.ContainsFunc(h.ID, unicode.IsControl) || seen[h.ID] || !filepath.IsAbs(h.Executable) || !digest(h.ExecutableSHA256) || h.ModelRevision == "" || h.OverheadRAMBytes == 0 || h.MaxOutputTokens < 1 || h.MaxOutputTokens > 65536 || h.Prices == nil {
			return bad
		}
		seen[h.ID] = true
		switch h.Kind {
		case "pi", "openclaw", "goose":
		case "hermes":
			if !filepath.IsAbs(h.HermesSourceDir) || !digest(h.RuntimeSHA256) {
				return bad
			}
		case "openhands":
			if !digest(h.RuntimeSHA256) {
				return bad
			}
		default:
			return bad
		}
		for _, n := range []float64{h.Prices.Input, h.Prices.Output, h.Prices.CacheRead, h.Prices.CacheWrite} {
			if n < 0 || math.IsInf(n, 0) || math.IsNaN(n) {
				return bad
			}
		}
		found := false
		for _, m := range s.Models {
			if m.ID == h.ModelID {
				for _, p := range s.Providers {
					if p.ID == m.Provider && (p.Kind == "ollama" || p.Kind == "openai_compatible") {
						found = true
					}
				}
			}
		}
		if !found {
			return bad
		}
	}
	return nil
}

// Copy the registration slice so inspection never mutates execution settings.
func (s *Settings) redactNativeHarnessPaths() {
	if s.NativeHarnessEvidenceDir != "" {
		s.NativeHarnessEvidenceDir = "[REDACTED]"
	}
	s.NativeHarnesses = append([]NativeHarness(nil), s.NativeHarnesses...)
	for i := range s.NativeHarnesses {
		s.NativeHarnesses[i].Executable = "[REDACTED]"
		if s.NativeHarnesses[i].HermesSourceDir != "" {
			s.NativeHarnesses[i].HermesSourceDir = "[REDACTED]"
		}
	}
}
