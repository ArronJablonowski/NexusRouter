package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserSkillsUnconfigured(t *testing.T) {
	s := &Service{}
	p, err := s.BrowserSkills(context.Background())
	if err != nil || p.Version != 1 || p.Enabled || p.Scope != "Not configured" || p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("invalid empty library: %+v %v", p, err)
	}
}

func TestBrowserSkillsEmptyExistingStore(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.settings.Skills.Root = root
	s.settings.Skills.Scope = "project"
	p, err := s.BrowserSkills(context.Background())
	if err != nil || p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("empty store: %+v %v", p, err)
	}
}
