package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectToolAccessAtomicUpdate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	path := file(t, "version: 1\nmode: hybrid\nworkers:\n  delegate_model: local-worker\n  delegate_read_tools: false\nmodels:\n  - id: local-worker\n    provider: local\n    model: fixture\n    locality: local\n    capabilities: [chat]\n    context_tokens: 4096\n    estimated_cost: 0\nproviders:\n  - id: local\n    kind: ollama\ntools:\n  enabled: false\n  max_turns: 8\n")
	before, digest, err := ReadProjectToolAccess(path)
	if err != nil || before.Enabled || before.DelegateReadTools || before.ReadRoot != "" || before.SpecialistsAllowCloud || len(digest) != 64 {
		t.Fatal(before, digest, err)
	}
	next := ToolAccess{Enabled: true, DelegateReadTools: true, ReadRoot: root, SpecialistsAllowCloud: true}
	saved, nextDigest, err := UpdateProjectToolAccess(path, digest, next)
	if err != nil || saved != next || nextDigest == digest {
		t.Fatal(saved, nextDigest, err)
	}
	loaded, err := Load(Options{ProjectFile: path})
	if err != nil || toolAccess(loaded) != next {
		t.Fatal(loaded.Tools, loaded.Workers, err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "read_root: "+root) || !strings.Contains(string(body), "delegate_read_tools: true") || !strings.Contains(string(body), "specialists_allow_cloud: true") {
		t.Fatal(string(body))
	}
	if _, _, err := UpdateProjectToolAccess(path, digest, ToolAccess{}); !errors.Is(err, ErrConfigConflict) {
		t.Fatal("stale update accepted", err)
	}
}

func TestProjectToolAccessRejectsInvalidAndSymlink(t *testing.T) {
	path := file(t, "version: 1\ntools:\n  enabled: false\n  max_turns: 8\n")
	_, digest, err := ReadProjectToolAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = UpdateProjectToolAccess(path, digest, ToolAccess{Enabled: true, ReadRoot: "relative"}); err == nil {
		t.Fatal("relative root accepted")
	}
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ReadProjectToolAccess(link); !errors.Is(err, ErrConfigWrite) {
		t.Fatal("symlink accepted", err)
	}
}
