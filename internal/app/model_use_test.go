package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
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

func TestDisabledClassifierDoesNotRunAuxiliaryInference(t *testing.T) {
	svc, cfg, calls := intentClassifierService(t, false)
	if _, err := config.UpdateModelUse(cfg.Telemetry.Database, "local", cfg.Routing.Classifier.ModelID, false); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Run(context.Background(), Request{Prompt: "please handle this ambiguous request"})
	if calls.Load() != 0 {
		t.Fatalf("disabled classifier received %d new calls", calls.Load())
	}
	if err != nil || out.Text != "routed answer" {
		t.Fatalf("deterministic routing should remain available: %+v %v", out, err)
	}
}

func TestSkippedClassifierRemainsSkippedAcrossAdmissionRetries(t *testing.T) {
	svc, cfg, calls := intentClassifierService(t, false)
	if _, err := config.UpdateModelUse(cfg.Telemetry.Database, "local", cfg.Routing.Classifier.ModelID, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := Request{Prompt: "ambiguous request", intentClassification: &intentClassificationState{}}
	for range 2 {
		r, err = svc.prepareAuxiliaryIntent(ctx, db, r)
		if err != nil || calls.Load() != 0 || !r.intentClassification.skipped {
			t.Fatal("skipped classification changed during admission retry", err, calls.Load())
		}
	}
}
