package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestWorkflowDiscoveryEmptyReadOnlyAndPolicy(t *testing.T) {
	for _, mode := range []string{"empty", "missing", "disabled", "auto-disabled", "invalid-domain", "invalid-cursor", "invalid-limit", "secret-domain"} {
		t.Run(mode, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			svc.settings.Skills.Enabled, svc.settings.Skills.AutoDraft = true, true
			svc.settings.Skills.Root = filepath.Join(t.TempDir(), "unopened")
			svc.settings.Skills.Scope = "project"
			domain, after, limit := "creative", "", 5
			if mode != "missing" {
				store, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "disabled":
				svc.settings.Skills.Enabled = false
			case "auto-disabled":
				svc.settings.Skills.AutoDraft = false
			case "invalid-domain":
				domain = "../private-domain"
			case "invalid-cursor":
				after = "invalid:cursor"
			case "invalid-limit":
				limit = 21
			case "secret-domain":
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return domain
					}
					return ""
				}
			}
			page, err := svc.DiscoverSkillWorkflows(context.Background(), domain, after, limit)
			if mode == "empty" {
				if err != nil || page.Validate(after, limit) != nil {
					t.Fatal("valid empty discovery failed", page, err)
				}
			} else if err != ErrAdmission || !reflect.DeepEqual(page, skills.WorkflowCandidatePage{}) {
				t.Fatal("unsafe discovery failure", page, err)
			}
			if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
				t.Fatal("discovery opened/created skill root", err)
			}
			if mode == "missing" {
				if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
					t.Fatal("discovery created missing database", err)
				}
			}
		})
	}
}

func TestWorkflowDiscoveryInvalidContextAndService(t *testing.T) {
	var nilService *Service
	if page, err := nilService.DiscoverSkillWorkflows(context.Background(), "creative", "", 5); err != ErrAdmission || !reflect.DeepEqual(page, skills.WorkflowCandidatePage{}) {
		t.Fatal(page, err)
	}
	svc, _ := autoFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if page, err := svc.DiscoverSkillWorkflows(ctx, "creative", "", 5); err != ErrAdmission || !reflect.DeepEqual(page, skills.WorkflowCandidatePage{}) {
			t.Fatal(page, err)
		}
	}
}
