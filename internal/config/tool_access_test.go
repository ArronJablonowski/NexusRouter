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
	if err != nil || before.Enabled || before.DelegateReadTools || before.ReadRoot != Defaults().Tools.ReadRoot || before.SpecialistsAllowCloud || len(digest) != 64 {
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

func TestProjectSkillControlsRoundTrip(t *testing.T) {
	path := file(t, "version: 1\n")
	next, digest, err := ReadProjectToolAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	next.SkillsEnabled = true
	next.SkillsAutoDraft = true
	next.SkillsRoot = filepath.Join(t.TempDir(), "skills")
	next.SkillsScope = "project"
	saved, digest, err := UpdateProjectToolAccess(path, digest, next)
	if err != nil || saved != next {
		t.Fatal(saved, err)
	}
	loaded, err := Load(Options{ProjectFile: path})
	if err != nil || !loaded.Skills.Enabled || !loaded.Skills.AutoDraft || loaded.Skills.Root != next.SkillsRoot {
		t.Fatal(err)
	}
	next.SkillsEnabled = false
	next.SkillsAutoDraft = false
	saved, _, err = UpdateProjectToolAccess(path, digest, next)
	if err != nil || saved != next {
		t.Fatal(saved, err)
	}
	if _, err = os.Stat(next.SkillsRoot); !os.IsNotExist(err) {
		t.Fatal("settings save created or changed store")
	}
}

func TestProjectAdvertisementSettingsPersistAndRejectStale(t *testing.T) {
	path := file(t, "version: 1\nskills:\n  enabled: false\ntools:\n  enabled: false\n  max_turns: 8\n")
	saved, digest, err := ReadProjectToolAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	saved.RemoteAdvertisement.Enabled = true
	saved.RemoteAdvertisement.Interface = "en1"
	saved.RemoteAdvertisement.Name = "node-a"
	saved.RemoteAdvertisement.SSHPort = 2222
	result, next, err := UpdateProjectToolAccess(path, digest, saved)
	if err != nil || result != saved || next == digest {
		t.Fatal(result, err)
	}
	loaded, err := Load(Options{ProjectFile: path})
	if err != nil || loaded.RemoteAdvertisement != saved.RemoteAdvertisement {
		t.Fatal(loaded.RemoteAdvertisement, err)
	}
	if _, _, err = UpdateProjectToolAccess(path, digest, ToolAccess{}); !errors.Is(err, ErrConfigConflict) {
		t.Fatal("stale changed advertisement", err)
	}
	saved.RemoteAdvertisement.SSHPort = 70000
	if _, _, err = UpdateProjectToolAccess(path, next, saved); err == nil {
		t.Fatal("invalid persisted")
	}
	loaded, err = Load(Options{ProjectFile: path})
	if err != nil || loaded.RemoteAdvertisement.SSHPort != 2222 {
		t.Fatal("invalid changed file", err)
	}
}

func TestProjectToolAccessRejectsMalformedSectionsWithoutMutation(t *testing.T) {
	for _, section := range []string{"hardware", "telemetry", "remote_advertisement", "tools", "workers", "web_ui", "skills"} {
		t.Run(section, func(t *testing.T) {
			body := "version: 1\n" + section + ": [mac_memory_percent]\n"
			path := file(t, body)
			if _, _, err := UpdateProjectToolAccess(path, configDigest([]byte(body)), ToolAccess{}); err == nil {
				t.Fatal("malformed section accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != body {
				t.Fatal("invalid configuration modified", err)
			}
		})
	}
}
