package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func seedTaskSkills(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "skills")
	s, err := skills.Open(root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, item := range []struct {
		name, tag string
		active    bool
	}{{"active", "code", true}, {"draft", "code", false}, {"unrelated", "creative", true}} {
		d := skills.Draft{Key: skills.Key{Scope: "project", Name: item.name}, Privacy: skills.PrivacyPublic, Description: item.name + " workflow", Tags: []string{item.tag}, SourceSessions: []string{"source"}, Steps: []string{item.name + "-procedure secret-token"}, ValidationCases: []string{"fixture"}}
		v, err := s.Draft(context.Background(), d, false)
		if err != nil {
			t.Fatal(err)
		}
		if item.active {
			validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				return skills.Evidence{ID: "fixture", Passed: true, Deterministic: true}, nil
			})
			if err := s.Activate(context.Background(), d.Key, v.ID, "", validator, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func TestActiveSkillContextIsScopedRedactedAndPrivacyBound(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Skills.Root = seedTaskSkills(t)
	svc.settings.Skills.Scope = "project"
	svc.settings.Mode = "hybrid"
	svc.settings.Models[0].Locality = "cloud"
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "secret-token"
		}
		return ""
	}
	var seen, selected string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(body)
		seen = string(raw)
		selected, _ = body["model"].(string)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "help", Domain: "code"}); err != nil {
		t.Fatal(err)
	}
	if selected != "z" || !strings.Contains(seen, "active-procedure") || !strings.Contains(seen, "[REDACTED]") {
		t.Fatal("active skill missing or privacy ignored", selected, seen)
	}
	for _, bad := range []string{"draft-procedure", "unrelated-procedure", "secret-token"} {
		if strings.Contains(seen, bad) {
			t.Fatal("unexpected skill context", bad)
		}
	}
	if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "help", Domain: "code"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "active-procedure") {
		t.Fatal("local skill reached cloud-designated model")
	}
	svc.settings.Skills.LocalOnly = false
	if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "help", Domain: "code"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "active-procedure") {
		t.Fatal("explicit skill sharing ignored")
	}
}

func TestSkillContextParticipatesInAutomaticContextAdmission(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Skills.Root = seedTaskSkills(t)
	svc.settings.Skills.Scope = "project"
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 1200
	}
	if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "hi", Domain: "code"}); err == nil {
		t.Fatal("skill context omitted from admission budget")
	}
	svc.settings.Skills.Enabled = false
	if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "hi", Domain: "code"}); err != nil {
		t.Fatal("disabled skill still consumed context", err)
	}
}
