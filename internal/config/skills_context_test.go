package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSkillsContextRootRedactedWithoutMutation(t *testing.T) {
	s := Defaults()
	s.Skills.Root, s.Skills.Scope = "/private/project/skills", "project"
	want := s.Skills
	data, err := s.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), want.Root) {
		t.Fatal("skills root leaked")
	}
	var got Settings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if s.Skills != want {
		t.Fatalf("redaction mutated original: %#v", s.Skills)
	}
	want.Root = "[REDACTED]"
	if got.Skills != want {
		t.Fatalf("redacted skills=%#v want %#v", got.Skills, want)
	}
}

func TestSkillsContextDefaults(t *testing.T) {
	s, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := Skills{GenerationBudget: Defaults().Skills.GenerationBudget, Enabled: true, AutoDraft: true, AutoActivate: true, Rollback: true, LocalOnly: true, MaxSkills: 3, MaxBytes: 16384}
	if s.Skills != want {
		t.Fatalf("skills=%#v want %#v", s.Skills, want)
	}
}

func TestSkillsContextLayeredConfiguration(t *testing.T) {
	s, err := Load(Options{
		UserFile:    file(t, "skills:\n  root: /user/skills\n  scope: user-scope\n  max_skills: 4\n  max_bytes: 1024\n"),
		ProjectFile: file(t, "skills:\n  root: /project/skills\n  max_skills: 12\n  local_only: false\n"),
		Env:         Environment([]string{"DARWIN__SKILLS__SCOPE=environment-scope", "DARWIN__SKILLS__MAX_BYTES=2048"}),
		Flags:       map[string]string{"skills.scope": "flag-scope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Skills{GenerationBudget: Defaults().Skills.GenerationBudget, Enabled: true, AutoDraft: true, AutoActivate: true, Rollback: true, Root: "/project/skills", Scope: "flag-scope", MaxSkills: 12, MaxBytes: 2048}
	if s.Skills != want {
		t.Fatalf("skills=%#v want %#v", s.Skills, want)
	}
	s, err = Load(Options{UserFile: file(t, "skills:\n  root: /project/skills\n  scope: project\n"), ProjectFile: file(t, "skills:\n  root: ''\n  scope: ''\n")})
	if err != nil || s.Skills.Root != "" || s.Skills.Scope != "" {
		t.Fatalf("explicit empty pair must disable context: skills=%#v error=%v", s.Skills, err)
	}
}

func TestSkillsContextValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Settings)
		valid  bool
	}{
		{"configured", func(s *Settings) { s.Skills.Root = "/nonexistent/skills"; s.Skills.Scope = "Project_1-v2" }, true},
		{"root only", func(s *Settings) { s.Skills.Root = "/skills" }, false},
		{"scope only", func(s *Settings) { s.Skills.Scope = "project" }, false},
		{"relative root", func(s *Settings) { s.Skills.Root = "skills"; s.Skills.Scope = "project" }, false},
		{"filesystem root", func(s *Settings) { s.Skills.Root = "/"; s.Skills.Scope = "project" }, false},
		{"normalized root", func(s *Settings) { s.Skills.Root = "/skills/.."; s.Skills.Scope = "project" }, false},
		{"minimum limits", func(s *Settings) { s.Skills.MaxSkills = 1; s.Skills.MaxBytes = 256 }, true},
		{"maximum limits", func(s *Settings) { s.Skills.MaxSkills = 16; s.Skills.MaxBytes = 65536 }, true},
		{"zero skills", func(s *Settings) { s.Skills.MaxSkills = 0 }, false},
		{"excessive skills", func(s *Settings) { s.Skills.MaxSkills = 17 }, false},
		{"insufficient bytes", func(s *Settings) { s.Skills.MaxBytes = 255 }, false},
		{"excessive bytes", func(s *Settings) { s.Skills.MaxBytes = 65537 }, false},
		{"disabled invalid limits", func(s *Settings) { s.Skills.Enabled = false; s.Skills.MaxSkills = -1 }, false},
		{"local mode configured shareable", func(s *Settings) {
			s.Mode = "local_only"
			s.Skills.Root = "/skills"
			s.Skills.Scope = "project"
			s.Skills.LocalOnly = false
		}, false},
		{"local mode unconfigured", func(s *Settings) { s.Mode = "local_only"; s.Skills.LocalOnly = false }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Defaults()
			tc.change(&s)
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	for _, scope := range []string{" ", "a b", "a.b", "a/b", "a\x00b", "_a", strings.Repeat("s", 65)} {
		s := Defaults()
		s.Skills.Root, s.Skills.Scope = "/skills", scope
		if s.Validate() == nil {
			t.Fatalf("invalid scope accepted: %q", scope)
		}
	}
	s := Defaults()
	s.Skills.Root, s.Skills.Scope = "/skills", strings.Repeat("s", 64)
	if err := s.Validate(); err != nil {
		t.Fatal("maximum length scope rejected", err)
	}
}
