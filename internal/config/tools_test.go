package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolsDefaults(t *testing.T) {
	s := Defaults()
	if s.Tools.Enabled || s.Tools.ReadRoot != "" || s.Tools.MaxTurns != 8 {
		t.Fatalf("tools defaults = %#v", s.Tools)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestToolsValidation(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "not-created")
	for _, tc := range []struct {
		name  string
		tools Tools
		valid bool
	}{
		{"enabled absolute nonexistent root", Tools{true, missingRoot, 8}, true},
		{"enabled minimum turns", Tools{true, missingRoot, 2}, true},
		{"enabled maximum turns", Tools{true, missingRoot, 32}, true},
		{"enabled empty root", Tools{true, "", 8}, false},
		{"enabled relative root", Tools{true, "relative", 8}, false},
		{"enabled tilde root", Tools{true, "~/project", 8}, false},
		{"disabled empty root", Tools{false, "", 8}, true},
		{"disabled relative root", Tools{false, "relative", 8}, true},
		{"disabled zero turns", Tools{false, "", 0}, false},
		{"disabled negative turns", Tools{false, "", -1}, false},
		{"disabled one turn", Tools{false, "", 1}, false},
		{"disabled excessive turns", Tools{false, "", 33}, false},
		{"enabled one turn", Tools{true, missingRoot, 1}, false},
		{"enabled excessive turns", Tools{true, missingRoot, 33}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Defaults()
			s.Tools = tc.tools
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatalf("validation error = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func TestToolsReadRootRedactedWithoutMutatingSettings(t *testing.T) {
	s := Defaults()
	s.Tools = Tools{true, "/private/project/read-root", 12}
	data, err := s.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), s.Tools.ReadRoot) {
		t.Fatal("tool read root leaked")
	}
	var got Settings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Tools.ReadRoot != "[REDACTED]" || !got.Tools.Enabled || got.Tools.MaxTurns != 12 || s.Tools.ReadRoot != "/private/project/read-root" {
		t.Fatalf("redacted tools=%#v original tools=%#v", got.Tools, s.Tools)
	}
}

func TestToolsLoadScalarOverrides(t *testing.T) {
	s, err := Load(Options{Flags: map[string]string{"tools.enabled": "true", "tools.read_root": "/test/read-root", "tools.max_turns": "12"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Tools != (Tools{true, "/test/read-root", 12}) {
		t.Fatalf("loaded tools = %#v", s.Tools)
	}
}
