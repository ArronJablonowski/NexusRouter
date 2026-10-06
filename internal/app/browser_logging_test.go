package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestBrowserLoggingMetadataOnly(t *testing.T) {
	s := submissionService(t)
	dir := t.TempDir()
	s.settings.Telemetry.Database = filepath.Join(dir, "runtime.db")
	s.settings.Telemetry.MetricsExport = &config.MetricsExport{Enabled: false, Endpoint: "https://private.example/path-secret", APIKeyEnv: "PRIVATE_KEY_NAME"}
	if err := os.WriteFile(filepath.Join(dir, "darwin.log"), []byte("PRIVATE_LOG_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := s.BrowserLogging(context.Background())
	if err != nil || p.Validate() != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(p)
	for _, secret := range []string{"PRIVATE_LOG_CONTENT", "path-secret", "PRIVATE_KEY_NAME"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("private contents escaped metadata boundary")
		}
	}
	seen := map[string]bool{}
	for _, x := range p.Items {
		seen[x.ID] = true
		if x.ID == "runtime" && (x.Location != s.settings.Telemetry.Database || x.Status != "Not present") {
			t.Fatal("configured database metadata incorrect")
		}
		if x.ID == "metrics" && x.Status != "Disabled" {
			t.Fatal("disabled exporter reported active")
		}
	}
	if !seen["runtime"] || !seen["application"] || !seen["collector"] {
		t.Fatal("missing locations")
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("metadata read created runtime database")
	}
}
