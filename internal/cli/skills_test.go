package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func cliSkillPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(p, "skills")
}

func cliSkillDraft() skills.Draft {
	return skills.Draft{Key: skills.Key{Scope: "project", Name: "checks"}, Description: "Run checks", SourceSessions: []string{"session1"}, Steps: []string{"private workflow body"}, ValidationCases: []string{"fixture"}}
}

func TestSkillsDraftInspectionAndScope(t *testing.T) {
	p := cliSkillPath(t)
	d := cliSkillDraft()
	b, _ := json.Marshal(d)
	var out, diagnostics bytes.Buffer
	invoke := func(command string, extra ...string) int {
		out.Reset()
		diagnostics.Reset()
		args := append([]string{command, "--root", p, "--scope", "project"}, extra...)
		return runSkills(args, bytes.NewReader(b), &out, &diagnostics)
	}
	if code := invoke("draft"); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	var v skills.Version
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if code := invoke("list"); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatal(code, out.String())
	}
	if code := invoke("history", "--name", d.Key.Name); code != 0 || !strings.Contains(out.String(), v.ID) || strings.Contains(out.String(), d.Steps[0]) {
		t.Fatal(code, out.String())
	}
	if code := invoke("show", "--name", d.Key.Name, "--version", v.ID); code != 0 || !strings.Contains(out.String(), d.Steps[0]) {
		t.Fatal(code, out.String())
	}
	if code := invoke("show", "--name", d.Key.Name); code != 1 {
		t.Fatal("draft became active", code)
	}
	if code := invoke("show", "--name", d.Key.Name, "--version", "../secret"); code != 2 {
		t.Fatal(code)
	}
	if code := runSkills([]string{"history", "--root", p, "--scope", "other", "--name", d.Key.Name}, nil, &out, &diagnostics); code != 1 {
		t.Fatal("scope leak", code)
	}
	// Inspection remains functional without the mutation lock and does not recreate it.
	if err := os.Remove(filepath.Join(p, "lock")); err != nil {
		t.Fatal(err)
	}
	if code := invoke("history", "--name", d.Key.Name); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(p, "lock")); !os.IsNotExist(err) {
		t.Fatal("inspection created lock", err)
	}
}

func TestSkillsRejectedRequestsNeverInitializeStore(t *testing.T) {
	for _, args := range [][]string{
		{"list"}, {"show", "--name", "checks"}, {"history", "--name", "checks"},
		{"rollback", "--name", "checks", "--expected-version", strings.Repeat("a", 32)},
		{"activate", "--passed=true"}, {"draft", "--automatic"},
		{"list", "--scope", "../escape"}, {"show", "--name", "../escape"},
	} {
		p := cliSkillPath(t)
		full := append([]string{args[0], "--root", p, "--scope", "project"}, args[1:]...)
		var out bytes.Buffer
		if code := runSkills(full, strings.NewReader("{}"), &out, &out); code == 0 {
			t.Fatal(full)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("created store", args, err)
		}
	}
	for _, input := range []string{"{}", `{"key":{"scope":"other","name":"checks"}}`, `{"passed":true}`, "{} {}", strings.Repeat("x", (256<<10)+1)} {
		p := cliSkillPath(t)
		if code := runSkills([]string{"draft", "--root", p, "--scope", "project"}, strings.NewReader(input), io.Discard, io.Discard); code != 2 {
			t.Fatal(code)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("invalid input created store", err)
		}
	}
}

func TestSkillsRollbackRequiresCurrentVersion(t *testing.T) {
	p := cliSkillPath(t)
	s, err := skills.Open(p, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	d := cliSkillDraft()
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "fixture", Passed: true, Deterministic: true}, nil
	})
	v1, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(ctx, d.Key, v1.ID, "", validator, false); err != nil {
		t.Fatal(err)
	}
	v2, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(ctx, d.Key, v2.ID, v1.ID, validator, false); err != nil {
		t.Fatal(err)
	}
	invoke := func(expected string) int {
		return runSkills([]string{"rollback", "--root", p, "--scope", "project", "--name", d.Key.Name, "--expected-version", expected}, nil, io.Discard, io.Discard)
	}
	if code := invoke(v1.ID); code != 1 {
		t.Fatal(code)
	}
	if code := invoke(v2.ID); code != 0 {
		t.Fatal(code)
	}
	active, err := s.Load(ctx, d.Key, "")
	if err != nil || active.ID != v1.ID {
		t.Fatal(active, err)
	}
	if code := invoke(v2.ID); code != 1 {
		t.Fatal("stale rollback succeeded", code)
	}
	if _, err := s.Draft(ctx, d, true); !errors.Is(err, skills.ErrDisabled) {
		t.Fatal("automatic enabled", err)
	}
}

type skillBrokenWriter struct{}

func (skillBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("fixture") }

func TestSkillsOutputAndGenericErrors(t *testing.T) {
	if code := runSkills(nil, nil, io.Discard, skillBrokenWriter{}); code != 1 {
		t.Fatal(code)
	}
	p := cliSkillPath(t)
	b, _ := json.Marshal(cliSkillDraft())
	if code := runSkills([]string{"draft", "--root", p, "--scope", "project"}, bytes.NewReader(b), skillBrokenWriter{}, io.Discard); code != 1 {
		t.Fatal(code)
	}
	var out bytes.Buffer
	if code := runSkills([]string{"list", "--root", p, "--scope", "project", "--secret-token"}, nil, &out, &out); code != 2 || strings.Contains(out.String(), "secret-token") {
		t.Fatal(code, out.String())
	}
}
