package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

func integerSettingYAML(path, value string) string {
	if strings.HasPrefix(path, "models.") {
		return "providers:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434\nmodels:\n  - id: fixture\n    provider: local\n    model: fixture\n    locality: local\n    capabilities: [chat]\n    " + strings.TrimPrefix(path, "models.") + ": " + value + "\n"
	}
	section, field, nested := strings.Cut(path, ".")
	if !nested {
		return path + ": " + value + "\n"
	}
	return section + ":\n  " + field + ": " + value + "\n"
}

func TestIntegerSettingsRejectYAMLNonIntegerTypes(t *testing.T) {
	for _, path := range []string{
		"version", "workers.max_in_process", "tools.max_turns", "routing.minimum_samples",
		"memory.max_facts", "memory.max_bytes", "skills.max_skills", "skills.max_bytes",
		"models.context_tokens", "models.ram_bytes", "models.vram_bytes",
	} {
		for _, value := range []string{"1.5", "1.0", "'8'", "true", "secret-value", "99999999999999999999999999999999999"} {
			t.Run(path+"/"+value, func(t *testing.T) {
				_, err := Load(Options{ProjectFile: file(t, integerSettingYAML(path, value))})
				if err == nil {
					t.Fatalf("accepted noninteger %s=%s", path, value)
				}
				if strings.Contains(err.Error(), "secret-value") {
					t.Fatal("integer type error leaked supplied value")
				}
			})
		}
	}
}

func TestUnsignedModelSettingsRejectNegativeAndOverflow(t *testing.T) {
	for _, field := range []string{"models.ram_bytes", "models.vram_bytes"} {
		for _, value := range []string{"-1", "18446744073709551616"} {
			if _, err := Load(Options{ProjectFile: file(t, integerSettingYAML(field, value))}); err == nil {
				t.Fatalf("accepted invalid uint64 %s=%s", field, value)
			}
		}
	}
}

func TestIntegerSettingsAcceptFileBoundaries(t *testing.T) {
	maxInt := strconv.FormatUint(uint64(^uint(0)>>1), 10)
	for _, tc := range []struct{ path, value string }{
		{"version", "1"}, {"workers.max_in_process", "1"}, {"tools.max_turns", "2"}, {"tools.max_turns", "32"},
		{"routing.minimum_samples", "1"}, {"memory.max_facts", "1"}, {"memory.max_facts", "64"},
		{"memory.max_bytes", "256"}, {"memory.max_bytes", "65536"}, {"skills.max_skills", "1"},
		{"skills.max_skills", "16"}, {"skills.max_bytes", "256"}, {"skills.max_bytes", "65536"},
		{"models.context_tokens", "0"}, {"models.context_tokens", maxInt},
		{"models.ram_bytes", "0"}, {"models.ram_bytes", "18446744073709551615"},
		{"models.vram_bytes", "0"}, {"models.vram_bytes", "18446744073709551615"},
	} {
		t.Run(tc.path+"/"+tc.value, func(t *testing.T) {
			s, err := Load(Options{ProjectFile: file(t, integerSettingYAML(tc.path, tc.value))})
			if err != nil {
				t.Fatal(err)
			}
			if tc.path == "models.ram_bytes" && tc.value != "0" && s.Models[0].RAMBytes != math.MaxUint64 {
				t.Fatal("uint64 boundary lost precision")
			}
			if tc.path == "models.vram_bytes" && tc.value != "0" && s.Models[0].VRAMBytes != math.MaxUint64 {
				t.Fatal("uint64 boundary lost precision")
			}
		})
	}
}

func TestIntegerOverrideTypesAndBoundaries(t *testing.T) {
	for _, layer := range []string{"env", "flags"} {
		for _, tc := range []struct{ path, value string }{
			{"tools.max_turns", "2"}, {"tools.max_turns", "32"}, {"memory.max_facts", "64"},
			{"memory.max_bytes", "65536"}, {"skills.max_skills", "16"}, {"skills.max_bytes", "256"},
		} {
			o := Options{}
			values := map[string]string{tc.path: tc.value}
			if layer == "env" {
				o.Env = values
			} else {
				o.Flags = values
			}
			if _, err := Load(o); err != nil {
				t.Fatalf("valid %s override %s=%s: %v", layer, tc.path, tc.value, err)
			}
		}
		for _, value := range []string{"1.5", "1.0", "true", "'8'", "secret-value", "99999999999999999999999999999999999"} {
			o := Options{}
			values := map[string]string{"workers.max_in_process": value}
			if layer == "env" {
				o.Env = values
			} else {
				o.Flags = values
			}
			if _, err := Load(o); err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("invalid %s override acceptance or leak: %v", layer, err)
			}
		}
	}
}

func TestShadowedInvalidIntegerLayersRejected(t *testing.T) {
	for _, value := range []string{"1.5", "'8'", "secret-value", "99999999999999999999999999999999999"} {
		t.Run(fmt.Sprintf("file/%s", value), func(t *testing.T) {
			_, err := Load(Options{UserFile: file(t, integerSettingYAML("workers.max_in_process", value)), ProjectFile: file(t, integerSettingYAML("workers.max_in_process", "8"))})
			if err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("shadowed invalid file acceptance or leak: %v", err)
			}
		})
		t.Run(fmt.Sprintf("env/%s", value), func(t *testing.T) {
			_, err := Load(Options{Env: map[string]string{"workers.max_in_process": value}, Flags: map[string]string{"workers.max_in_process": "8"}})
			if err == nil || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("shadowed invalid environment acceptance or leak: %v", err)
			}
		})
	}
}

func TestRealFloatSettingsRemainAccepted(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, "hardware:\n  max_ram_usage_pct: 80.5\nrouting:\n  exploration_rate: 0.125\n")})
	if err != nil || s.Hardware.MaxRAM != 80.5 || s.Routing.Exploration != .125 {
		t.Fatalf("real floats rejected or changed: %v", err)
	}
	s, err = Load(Options{ProjectFile: file(t, integerSettingYAML("models.estimated_cost", "0.125"))})
	if err != nil || s.Models[0].EstimatedCost == nil || *s.Models[0].EstimatedCost != .125 {
		t.Fatalf("pointer float rejected or changed: %v", err)
	}
}
