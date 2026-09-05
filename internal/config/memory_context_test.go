package config

import (
	"strings"
	"testing"
)

func TestMemoryContextDefaults(t *testing.T) {
	s, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{Enabled: true, LocalOnly: true, MaxFacts: 8, MaxBytes: 16384}
	if s.Memory != want {
		t.Fatalf("memory=%#v want %#v", s.Memory, want)
	}
}

func TestMemoryContextLayeredConfiguration(t *testing.T) {
	s, err := Load(Options{
		UserFile:    file(t, "memory:\n  scope: user-scope\n  max_facts: 4\n  max_bytes: 1024\n"),
		ProjectFile: file(t, "memory:\n  scope: project-scope\n  max_facts: 12\n"),
		Env:         Environment([]string{"DARWIN__MEMORY__SCOPE=environment-scope", "DARWIN__MEMORY__MAX_BYTES=2048"}),
		Flags:       map[string]string{"memory.scope": "flag-scope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{Enabled: true, LocalOnly: true, Scope: "flag-scope", MaxFacts: 12, MaxBytes: 2048}
	if s.Memory != want {
		t.Fatalf("memory=%#v want %#v", s.Memory, want)
	}
	s, err = Load(Options{UserFile: file(t, "memory:\n  scope: user-scope\n"), ProjectFile: file(t, "memory:\n  scope: ''\n")})
	if err != nil || s.Memory.Scope != "" {
		t.Fatalf("explicit empty scope must disable opt-in: memory=%#v error=%v", s.Memory, err)
	}
}

func TestMemoryContextValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Memory)
		valid  bool
	}{
		{"scope at limit", func(m *Memory) { m.Scope = strings.Repeat("s", 512) }, true},
		{"scope too long", func(m *Memory) { m.Scope = strings.Repeat("s", 513) }, false},
		{"scope whitespace", func(m *Memory) { m.Scope = " \t\n" }, false},
		{"scope NUL", func(m *Memory) { m.Scope = "project\x00scope" }, false},
		{"minimum limits", func(m *Memory) { m.MaxFacts = 1; m.MaxBytes = 256 }, true},
		{"maximum limits", func(m *Memory) { m.MaxFacts = 64; m.MaxBytes = 65536 }, true},
		{"zero facts empty scope", func(m *Memory) { m.MaxFacts = 0 }, false},
		{"negative facts", func(m *Memory) { m.MaxFacts = -1 }, false},
		{"excessive facts", func(m *Memory) { m.MaxFacts = 65 }, false},
		{"too few bytes", func(m *Memory) { m.MaxBytes = 255 }, false},
		{"negative bytes", func(m *Memory) { m.MaxBytes = -1 }, false},
		{"excessive bytes", func(m *Memory) { m.MaxBytes = 65537 }, false},
		{"disabled invalid limits", func(m *Memory) { m.Enabled = false; m.MaxFacts = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Defaults()
			tc.change(&s.Memory)
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	for _, yaml := range []string{
		"memory:\n  scope: null\n", "memory:\n  scope: ' '\n",
		"memory:\n  max_facts: 0\n", "memory:\n  max_facts: 65\n",
		"memory:\n  max_bytes: 255\n", "memory:\n  max_bytes: 65537\n", "memory:\n  max_bytes: null\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, yaml)}); err == nil {
			t.Fatalf("accepted invalid memory YAML %q", yaml)
		}
	}
}
