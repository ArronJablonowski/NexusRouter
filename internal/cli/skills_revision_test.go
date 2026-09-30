package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func cliRevisionFixture(t *testing.T) (*skills.FileStore, string, skills.Key, string, string, skills.ValidatorFunc) {
	t.Helper()
	path := cliSkillPath(t)
	s, err := skills.Open(path, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	validate := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
		return skills.Evidence{ID: "fixture", Passed: true, Deterministic: true}, nil
	})
	d := cliSkillDraft()
	a, err := s.Draft(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(context.Background(), d.Key, a.ID, "", validate, false); err != nil {
		t.Fatal(err)
	}
	d.Steps = []string{"second private workflow body"}
	b, err := s.Draft(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	return s, path, d.Key, a.ID, b.ID, validate
}

func cliRevisionState(t *testing.T, path string, key skills.Key) skills.ActivationState {
	t.Helper()
	var out, diagnostics bytes.Buffer
	if code := runSkills([]string{"state", "--root", path, "--scope", key.Scope, "--name", key.Name}, nil, &out, &diagnostics); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	var state skills.ActivationState
	if err := json.Unmarshal(out.Bytes(), &state); err != nil || state.Validate() != nil || state.Key != key {
		t.Fatal(out.String(), err)
	}
	if strings.Contains(out.String(), "private workflow") || strings.Contains(out.String(), "steps") || strings.Contains(out.String(), "validation_cases") {
		t.Fatal("state exposes workflow content", out.String())
	}
	return state
}

func TestSkillsStateIsReadOnlyAndWorkflowFree(t *testing.T) {
	s, path, key, a, _, _ := cliRevisionFixture(t)
	expected, err := s.ActivationState(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "lock")); err != nil {
		t.Fatal(err)
	}
	state := cliRevisionState(t, path, key)
	if state != expected || state.Active != a {
		t.Fatal("wrong activation state", state, expected)
	}
	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("state changed catalog", err)
	}
	if _, err := os.Stat(filepath.Join(path, "lock")); !os.IsNotExist(err) {
		t.Fatal("state recreated mutation lock", err)
	}
}

func TestSkillsRevisionRollbackAndABAProtection(t *testing.T) {
	s, path, key, a, b, validate := cliRevisionFixture(t)
	stale := cliRevisionState(t, path, key)
	if err := s.Activate(context.Background(), key, b, a, validate, false); err != nil {
		t.Fatal(err)
	}
	fresh := cliRevisionState(t, path, key)
	if fresh.Active != b || fresh.Revision == stale.Revision {
		t.Fatal("activation did not change revision")
	}
	var out, diagnostics bytes.Buffer
	args := []string{"rollback", "--root", path, "--scope", key.Scope, "--name", key.Name, "--expected-version", b, "--expected-revision", fresh.Revision}
	if code := runSkills(args, nil, &out, &diagnostics); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	returned := cliRevisionState(t, path, key)
	if returned.Active != a || returned.Revision == stale.Revision {
		t.Fatal("rollback failed or ABA revision reused", returned, stale)
	}
	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostics.Reset()
	args = []string{"rollback", "--root", path, "--scope", key.Scope, "--name", key.Name, "--expected-version", a, "--expected-revision", stale.Revision}
	if code := runSkills(args, nil, &out, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "changed") {
		t.Fatal("stale revision not recognized", code, diagnostics.String())
	}
	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("stale rollback changed catalog", err)
	}
	if actual := cliRevisionState(t, path, key); actual != returned {
		t.Fatal("stale rollback altered active state", actual, returned)
	}
}

func TestSkillsRevisionInvalidFlagsNeverCreateStore(t *testing.T) {
	version, revision := strings.Repeat("a", 32), strings.Repeat("b", 64)
	for _, extra := range [][]string{
		{"rollback", "--name", "checks", "--expected-revision", revision},
		{"rollback", "--name", "checks", "--expected-version", version, "--expected-revision", ""},
		{"rollback", "--name", "checks", "--expected-version", version, "--expected-revision", strings.Repeat("B", 64)},
		{"rollback", "--name", "checks", "--expected-version", version, "--expected-revision", strings.Repeat("b", 63)},
		{"rollback", "--name", "checks", "--expected-version", version, "--expected-revision", strings.Repeat("g", 64)},
		{"rollback", "--name", "checks", "--expected-version", version, "--expected-revision", revision, "--expected-revision", revision},
		{"rollback", "--name", "checks", "--expected-version", version, "--expected-version", version},
		{"state"},
		{"state", "--name", "checks", "--expected-revision", revision},
		{"state", "--name", "checks", "--expected-version", version},
		{"state", "--name", "checks", "--version", version},
		{"state", "--name", "checks", "--limit", "1"},
		{"state", "--name", "checks", "--name", "checks"},
		{"history", "--name", "checks", "--expected-revision", revision},
		{"show", "--name", "checks", "--expected-revision", revision},
		{"list", "--expected-revision", revision},
		{"draft", "--expected-revision", revision},
	} {
		path := cliSkillPath(t)
		args := append([]string{extra[0], "--root", path, "--scope", "project"}, extra[1:]...)
		if code := runSkills(args, strings.NewReader("{}"), io.Discard, io.Discard); code != 2 {
			t.Fatal("malformed precondition not rejected", args, code)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid flags created store", args, err)
		}
	}
}
