package cli

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunParsesNativeHarnessSelection(t *testing.T) {
	_, req, err := parseRunArgs([]string{"--config", "fixture.yaml", "--model", "chat", "--harness", "pi-local"})
	if err != nil || req.HarnessID != "pi-local" || req.ModelID != "chat" {
		t.Fatal(req, err)
	}
}

func TestOperatorHarnessEvidenceLifecycle(t *testing.T) {
	s := config.Defaults()
	s.Tools.Enabled = false
	service, err := app.NewService(s, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	closeStore, err := attachNativeHarnessEvidence(s, service)
	if err != nil || closeStore() != nil {
		t.Fatal("disabled store", err)
	}
	s.NativeHarnessEvidenceDir = filepath.Join(t.TempDir(), "evidence")
	closeStore, err = attachNativeHarnessEvidence(s, service)
	if err != nil {
		t.Fatal(err)
	}
	result, routeErr := service.Run(context.Background(), app.Request{HarnessID: "auto", ModelID: "auto", Prompt: "fixture", Domain: "writing", Profile: "fixture-v1", ContextTokens: 8192})
	if !errors.Is(routeErr, harness.ErrNoRoute) || result.TaskID != "" {
		t.Fatal("empty evidence must not dispatch", routeErr)
	}
	if err := closeStore(); err != nil {
		t.Fatal(err)
	}
	ledger, err := harness.OpenEvidenceStore(s.NativeHarnessEvidenceDir)
	if err != nil {
		t.Fatal("cannot reopen operator evidence", err)
	}
	defer ledger.Close()
	if _, err := ledger.Snapshot(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	s.NativeHarnessEvidenceDir = public
	if closer, err := attachNativeHarnessEvidence(s, service); err == nil {
		closer()
		t.Fatal("public evidence directory accepted")
	}
}
