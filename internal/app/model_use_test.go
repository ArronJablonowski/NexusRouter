package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"path/filepath"
	"testing"
)

func TestDisabledModelInventory(t *testing.T) {
	s := modelCatalogService()
	s.settings.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	if _, err := config.UpdateModelUse(s.settings.Telemetry.Database, "local", "chat", false); err != nil {
		t.Fatal(err)
	}
	page, err := s.BrowserModels(context.Background(), health.Report{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Models {
		if m.ID == "chat" {
			if m.Enabled || m.Usable || m.Health != "disabled" {
				t.Fatal(m)
			}
			return
		}
	}
	t.Fatal("model missing")
}

func TestDisabledModelRejectsExplicitAdmission(t *testing.T) {
	_, cfg := autoFixture(t)
	if _, err := config.UpdateModelUse(cfg.Telemetry.Database, "local", "z", false); err != nil {
		t.Fatal(err)
	}
	if _, err := RunExplicit(context.Background(), cfg, Request{ModelID: "z", Prompt: "test"}, nil); err == nil {
		t.Fatal("disabled model admitted")
	}
}
