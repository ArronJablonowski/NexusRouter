package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"unknown"}, 2},
		{[]string{"freeze", "--help"}, 0},
		{[]string{"freeze"}, 2},
		{[]string{"freeze", "--version", "1.0.0", "--commit", "bad", "--out", "x"}, 1},
		{[]string{"verify", "--help"}, 0},
		{[]string{"verify"}, 2},
		{[]string{"verify", "--record", "missing"}, 1},
		{[]string{"verify", "--record", "x", "extra"}, 2},
	} {
		var output bytes.Buffer
		if got := run(context.Background(), tc.args, &output); got != tc.code {
			t.Fatalf("%q: code %d, want %d (%s)", tc.args, got, tc.code, output.String())
		}
	}
}

func TestFreezeAndVerifyCommands(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, evidence := filepath.Join(base, "source"), filepath.Join(base, "evidence")
	for _, directory := range []string{source, evidence, filepath.Join(source, "docs"), filepath.Join(source, "examples")} {
		if err = os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for from, to := range map[string]string{
		"../../go.mod":                  "go.mod",
		"../../LICENSE":                 "LICENSE",
		"../../docs/release-install.md": "docs/release-install.md",
		"../../docs/release-notes.md":   "docs/release-notes.md",
		"../../examples/local.yaml":     "examples/local.yaml",
	} {
		body, readErr := os.ReadFile(from)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err = os.WriteFile(filepath.Join(source, to), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommand(t, source, "init")
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "-c", "user.name=Candidate Test", "-c", "user.email=candidate@example.invalid", "commit", "-m", "candidate fixture")
	commit := strings.TrimSpace(gitCommand(t, source, "rev-parse", "HEAD"))
	record := filepath.Join(evidence, "DarwinRouter_1.0.0_candidate.json")
	freeze := []string{"freeze", "--version", "1.0.0", "--commit", commit, "--source", source, "--out", record}
	var output bytes.Buffer
	if code := run(context.Background(), freeze, &output); code != 0 {
		t.Fatalf("freeze code %d: %s", code, output.String())
	}
	if code := run(context.Background(), []string{"verify", "--record", record, "--source", source}, &output); code != 0 {
		t.Fatalf("verify code %d: %s", code, output.String())
	}
	if code := run(context.Background(), freeze, &output); code != 1 {
		t.Fatalf("existing destination code %d, want 1", code)
	}
	dirty := filepath.Join(source, "untracked")
	if err = os.WriteFile(dirty, []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), []string{"verify", "--record", record, "--source", source}, &output); code != 1 {
		t.Fatalf("dirty checkout code %d, want 1", code)
	}
	if err = os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"unapproved"`), []byte(`"approved"`), 1)
	if err = os.WriteFile(record, body, 0644); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), []string{"verify", "--record", record, "--source", source}, &output); code != 1 {
		t.Fatalf("tampered record code %d, want 1", code)
	}
}

func gitCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1")
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, body)
	}
	return string(body)
}
