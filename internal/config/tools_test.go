package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolsDefaults(t *testing.T) {
	s := Defaults()
	if s.Tools.Enabled || s.Tools.WorkboardReadEnabled || s.Tools.ReadRoot != "" || s.Tools.MaxTurns != 8 {
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
		{"enabled absolute nonexistent root", Tools{Enabled: true, ReadRoot: missingRoot, MaxTurns: 8}, true},
		{"enabled minimum turns", Tools{Enabled: true, ReadRoot: missingRoot, MaxTurns: 2}, true},
		{"enabled maximum turns", Tools{Enabled: true, ReadRoot: missingRoot, MaxTurns: 32}, true},
		{"enabled empty root", Tools{Enabled: true, ReadRoot: "", MaxTurns: 8}, false},
		{"enabled relative root", Tools{Enabled: true, ReadRoot: "relative", MaxTurns: 8}, false},
		{"enabled tilde root", Tools{Enabled: true, ReadRoot: "~/project", MaxTurns: 8}, false},
		{"disabled empty root", Tools{Enabled: false, ReadRoot: "", MaxTurns: 8}, true},
		{"disabled relative root", Tools{Enabled: false, ReadRoot: "relative", MaxTurns: 8}, true},
		{"disabled zero turns", Tools{Enabled: false, ReadRoot: "", MaxTurns: 0}, false},
		{"disabled negative turns", Tools{Enabled: false, ReadRoot: "", MaxTurns: -1}, false},
		{"disabled one turn", Tools{Enabled: false, ReadRoot: "", MaxTurns: 1}, false},
		{"disabled excessive turns", Tools{Enabled: false, ReadRoot: "", MaxTurns: 33}, false},
		{"enabled one turn", Tools{Enabled: true, ReadRoot: missingRoot, MaxTurns: 1}, false},
		{"enabled excessive turns", Tools{Enabled: true, ReadRoot: missingRoot, MaxTurns: 33}, false},
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
	s.Tools = Tools{Enabled: true, ReadRoot: "/private/project/read-root", MaxTurns: 12}
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
	s, err := Load(Options{Flags: map[string]string{"tools.enabled": "true", "tools.read_root": "/test/read-root", "tools.workboard_read_enabled": "true", "tools.max_turns": "12"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Tools != (Tools{Enabled: true, ReadRoot: "/test/read-root", WorkboardReadEnabled: true, MaxTurns: 12}) {
		t.Fatalf("loaded tools = %#v", s.Tools)
	}
}
