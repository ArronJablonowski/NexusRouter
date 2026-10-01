package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeHarnessReadinessObservesPrerequisitesWithoutInference(t *testing.T) {
	calls := 0
	body := `{"models":[{"name":"fixture","model":"fixture"}]}`
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/tags" {
			t.Error("non inventory request", r.URL.Path)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	dir := t.TempDir()
	exe := filepath.Join(dir, "pi")
	data := []byte("fixture executable")
	if e := os.WriteFile(exe, data, 0700); e != nil {
		t.Fatal(e)
	}
	cost := 0.0
	cfg := config.Defaults()
	cfg.Tools.Enabled = false
	cfg.Telemetry.Database = filepath.Join(dir, "unused.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "FIXTURE_KEY"}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", ContextTokens: 32768, Capabilities: []string{"chat"}, EstimatedCost: &cost}}
	cfg.NativeHarnesses = []config.NativeHarness{{ID: "pair", Kind: "pi", ModelID: "chat", Executable: exe, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), ModelRevision: "v1", MaxOutputTokens: 1024, OverheadRAMBytes: 100, Prices: &config.NativeHarnessPrices{}}}
	s, e := NewService(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	key := ""
	s.secret = func(string) string { return key }
	check := func(state string) {
		t.Helper()
		r, e := s.NativeHarnessReadiness(context.Background(), "chat", "pair", 8192)
		if e != nil || r.ModelState != state {
			t.Fatal(r, e)
		}
	}
	check("unknown")
	if calls != 0 {
		t.Fatal("missing credential sent request")
	}
	key = "fixture"
	check("present")
	body = `{"models":[]}`
	check("absent")
	status = 503
	check("unknown")
	before := calls
	if e = os.WriteFile(exe, []byte("changed"), 0700); e != nil {
		t.Fatal(e)
	}
	check("unknown")
	if calls != before {
		t.Fatal("changed executable sent request")
	}
	if _, e = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(e) {
		t.Fatal("readiness persisted", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.NativeHarnessReadiness(ctx, "chat", "pair", 8192); e == nil {
		t.Fatal("ignored cancellation")
	}
}
