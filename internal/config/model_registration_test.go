package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudRegistrationPreservesSettingsAndStartsDisabled(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tasks.db")
	p := file(t, "version: 1\nmode: hybrid\ntelemetry:\n  database: "+db+"\nproviders:\n  - id: cloud\n    kind: openai_compatible\n    endpoint: https://example.com/v1\n    api_key_env: QA_CLOUD_KEY\nmodels:\n  - id: template\n    provider: cloud\n    model: old-model\n    locality: cloud\n    context_tokens: 8192\n    estimated_cost: 0.15\n    capabilities: [chat, tools]\nweb_ui:\n  default_model: template\n")
	_, digest, e := ReadProjectToolAccess(p)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(p)
	if _, e = RegisterCloudModel(p, "stale", "template", "new", "new-model", 4096); !errors.Is(e, ErrConfigConflict) {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("conflict changed configuration")
	}
	next, e := RegisterCloudModel(p, digest, "template", "new", "new-model", 4096)
	if e != nil || next == digest {
		t.Fatal(next, e)
	}
	s, e := Load(Options{ProjectFile: p})
	if e != nil || len(s.Models) != 2 {
		t.Fatal(s.Models, e)
	}
	m := s.Models[1]
	if m.ID != "new" || m.Provider != "cloud" || m.ContextTokens != 4096 || *m.EstimatedCost != .15 || strings.Join(m.Capabilities, ",") != "chat" || s.WebUI.DefaultModel != "template" || s.Providers[0].APIKeyEnv != "QA_CLOUD_KEY" {
		t.Fatal("template or unrelated settings changed", m)
	}
	if ModelUseAllowed(db, "local", "new") || !ModelUseAllowed(db, "local", "template") {
		t.Fatal("registration enabled unverified model")
	}
	if _, e = RegisterCloudModel(p, next, "template", "new", "different", 4096); !errors.Is(e, ErrConfigConflict) {
		t.Fatal("duplicate overwritten", e)
	}
	if _, e = RegisterCloudModel(p, next, "template", "invalid", "newer", 8193); e == nil {
		t.Fatal("template context exceeded")
	}
	link := filepath.Join(t.TempDir(), "link")
	if e = os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	if _, e = RegisterCloudModel(link, next, "template", "other", "x", 4096); e == nil {
		t.Fatal("symlink configuration accepted")
	}
}
