package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darwinrouter/internal/config"
	"darwinrouter/skills"
)

func contextSkillStore(t *testing.T) (*skills.FileStore, config.Skills) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(parent, "skills")
	s, err := skills.Open(p, []string{"project", "other"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	settings := config.Defaults().Skills
	settings.Enabled, settings.Root, settings.Scope = true, p, "project"
	settings.LocalOnly, settings.MaxSkills, settings.MaxBytes = true, 3, 16384
	return s, settings
}

func seedContextSkill(t *testing.T, s *skills.FileStore, scope, name, tag, body string, required []string, active bool) skills.Version {
	t.Helper()
	d := skills.Draft{Key: skills.Key{Scope: scope, Name: name}, Description: "Description " + name, Tags: []string{tag}, SourceSessions: []string{"session"}, Steps: []string{body}, RequiredTools: required, Risks: []string{"Review the result"}, ValidationCases: []string{"fixture"}, Configuration: "excluded configuration"}
	v, err := s.Draft(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
			return skills.Evidence{ID: "fixture-check", Passed: true, Deterministic: true}, nil
		})
		if err := s.Activate(context.Background(), d.Key, v.ID, v.Parent, validator, false); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func readContextSkills(t *testing.T, c *skillContext) []contextSkill {
	t.Helper()
	if c == nil || len(c.Messages) != 2 || c.Messages[0].Role != "system" || c.Messages[0].Content != skillInstruction || c.Messages[1].Role != "user" {
		t.Fatalf("invalid context: %+v", c)
	}
	var envelope struct {
		Skills []contextSkill `json:"procedural_skills"`
	}
	if err := json.Unmarshal([]byte(c.Messages[1].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Skills
}

func TestSkillContextActiveDomainScopeAndTools(t *testing.T) {
	s, settings := contextSkillStore(t)
	selected := seedContextSkill(t, s, "project", "a-selected", "code", "selected body", []string{"read_file"}, true)
	seedContextSkill(t, s, "other", "other-scope", "code", "wrong scope", nil, true)
	seedContextSkill(t, s, "project", "other-domain", "general", "wrong domain", nil, true)
	seedContextSkill(t, s, "project", "b-unavailable", "code", "unavailable tool", []string{"shell"}, true)
	draft := seedContextSkill(t, s, "project", "draft", "code", "unactivated body", nil, false)
	// Removing a draft body proves metadata discovery does not load drafts.
	if err := os.Remove(filepath.Join(settings.Root, "version-"+draft.ID+".json")); err != nil {
		t.Fatal(err)
	}
	c, err := loadSkillContext(context.Background(), settings, "code", []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := readContextSkills(t, c)
	if len(got) != 1 || got[0].Version != selected.ID || got[0].Steps[0] != "selected body" || !c.LocalOnly {
		t.Fatal(got, c)
	}
	if got[0].Configuration != "excluded configuration" {
		t.Fatal("configuration missing")
	}
	c, err = loadSkillContext(context.Background(), settings, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readContextSkills(t, c); len(got) != 1 || got[0].Key.Name != "other-domain" {
		t.Fatal(got)
	}
	c, err = loadSkillContext(context.Background(), settings, "unknown", nil, nil)
	if err != nil || c != nil {
		t.Fatal(c, err)
	}
}

func TestSkillContextWholeWorkflowBudgetAndVersion(t *testing.T) {
	s, settings := contextSkillStore(t)
	v1 := seedContextSkill(t, s, "project", "a-small", "general", "complete workflow", nil, true)
	settings.MaxSkills = 1
	first, err := loadSkillContext(context.Background(), settings, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(first.Messages)
	settings.MaxBytes = len(b)
	if c, err := loadSkillContext(context.Background(), settings, "", nil, nil); err != nil || len(readContextSkills(t, c)) != 1 {
		t.Fatal(c, err)
	}
	settings.MaxBytes--
	if c, err := loadSkillContext(context.Background(), settings, "", nil, nil); err != nil || c != nil {
		t.Fatal("truncated workflow", c, err)
	}
	settings.MaxBytes, settings.MaxSkills = 16384, 3
	seedContextSkill(t, s, "project", "b-large", "general", strings.Repeat("x", 20000), nil, true)
	c, err := loadSkillContext(context.Background(), settings, "", nil, nil)
	if err != nil || len(readContextSkills(t, c)) != 1 {
		t.Fatal(c, err)
	}
	v2 := seedContextSkill(t, s, "project", "a-small", "general", "replacement workflow", nil, true)
	if got := readContextSkills(t, first); got[0].Version != v1.ID || got[0].Steps[0] != "complete workflow" {
		t.Fatal("prepared snapshot changed", got)
	}
	c, err = loadSkillContext(context.Background(), settings, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readContextSkills(t, c); got[0].Version != v2.ID {
		t.Fatal(got)
	}
}

func TestSkillContextRedactsStringsBeforeJSONEncoding(t *testing.T) {
	s, settings := contextSkillStore(t)
	v := seedContextSkill(t, s, "project", "credential", "general", "quoted \"token\" and newline\nsecret", []string{"read_file"}, true)
	settings.LocalOnly = false
	secrets := []string{"credential", "project", "read_file", "session", "Review", v.ID, "\"token\"", "\nsecret", "excluded configuration"}
	c, err := loadSkillContext(context.Background(), settings, "", []string{"read_file"}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	got := readContextSkills(t, c)
	if c.LocalOnly || len(got) != 1 || got[0].Key.Scope != "[REDACTED]" || got[0].Version != "[REDACTED]" || got[0].Steps[0] != "quoted [REDACTED] and newline[REDACTED]" {
		t.Fatal(got, c)
	}
	if got[0].Key.Name != "[REDACTED]" || got[0].RequiredTools[0] != "[REDACTED]" || got[0].SourceSessions[0] != "[REDACTED]" || got[0].Risks[0] != "[REDACTED] the result" || got[0].Configuration != "[REDACTED]" {
		t.Fatal("unredacted field", got)
	}
}

func TestSkillContextReadOnlyMissingDisabledAndCorrupt(t *testing.T) {
	s, settings := contextSkillStore(t)
	v := seedContextSkill(t, s, "project", "skill", "general", "body", nil, true)
	if err := os.Remove(filepath.Join(settings.Root, "lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSkillContext(context.Background(), settings, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(settings.Root, "lock")); !os.IsNotExist(err) {
		t.Fatal("created lock", err)
	}
	missing := settings
	missing.Root = filepath.Join(filepath.Dir(settings.Root), "missing")
	if _, err := loadSkillContext(context.Background(), missing, "", nil, nil); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing.Root); !os.IsNotExist(err) {
		t.Fatal("created root", err)
	}
	missing.Enabled = false
	if got, err := loadSkillContext(context.Background(), missing, "", nil, nil); got != nil || err != nil {
		t.Fatal(got, err)
	}
	missing.Enabled, missing.Root = true, ""
	if got, err := loadSkillContext(context.Background(), missing, "", nil, nil); got != nil || err != nil {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(filepath.Join(settings.Root, "version-"+v.ID+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSkillContext(context.Background(), settings, "", nil, nil); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
}
