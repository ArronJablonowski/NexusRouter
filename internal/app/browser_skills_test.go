package app

import (
	"context"
	"testing"
)

func TestBrowserSkillsUnconfigured(t *testing.T) {
	s := &Service{}
	p, err := s.BrowserSkills(context.Background())
	if err != nil || p.Version != 1 || p.Enabled || p.Scope != "Not configured" || p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("invalid empty library: %+v %v", p, err)
	}
}
