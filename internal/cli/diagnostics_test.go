package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

type diagnosticShortWriter struct{}

func (diagnosticShortWriter) Write(body []byte) (int, error) { return len(body) - 1, nil }

func diagnosticFixture(t *testing.T, listen string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("version: 1\nmode: hybrid\ndaemon:\n  listen: %s\nproviders:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434\n    api_key_env: PRIVATE_PROVIDER_KEY\nmodels:\n  - id: local-fast\n    provider: local\n    model: fixture:latest\n    locality: local\n    capabilities: [chat]\n    context_tokens: 4096\n    ram_bytes: 1024\n", listen)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiagnosticCatalogsAreSafeAndStructured(t *testing.T) {
	path := diagnosticFixture(t, "127.0.0.1:7788")
	for _, command := range []string{"providers", "models"} {
		var out, stderr bytes.Buffer
		if code := RunWithInput([]string{command, "list", "--config", path}, strings.NewReader(""), &out, &stderr, "test"); code != 0 || stderr.Len() != 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("%s code=%d out=%q err=%q", command, code, out.String(), stderr.String())
		}
		for _, secret := range []string{"127.0.0.1:11434", "PRIVATE_PROVIDER_KEY"} {
			if strings.Contains(out.String(), secret) {
				t.Fatalf("%s leaked %q", command, secret)
			}
		}
		if command == "models" {
			var catalog routing.ModelCatalog
			if json.Unmarshal(out.Bytes(), &catalog) != nil || catalog.Validate() != nil || len(catalog.Models) != 1 || catalog.Models[0].Model != "fixture:latest" || catalog.Models[0].EstimatedCost != nil {
				t.Fatal("invalid native model catalog", out.String())
			}
		}
	}
	if code := RunWithInput([]string{"models", "list", "--config", path}, strings.NewReader(""), diagnosticShortWriter{}, &bytes.Buffer{}, "test"); code != 1 {
		t.Fatal("short model catalog write accepted", code)
	}
	for _, args := range [][]string{{"providers", "list"}, {"models", "list", "--config", path, "--config", path}, {"providers", "bad", "--config", path}} {
		if code := RunWithInput(args, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); code != 2 {
			t.Fatalf("invalid diagnostic arguments accepted: %v code=%d", args, code)
		}
	}
}

func TestDoctorReadsAuthenticatedValidatedDaemonHealth(t *testing.T) {
	now := time.Now().UTC()
	report := health.Report{Version: 1, CheckedAt: now, Status: "healthy", Ready: true, Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
		{Component: "model", ID: "local-fast", Status: "healthy", Code: "available"},
	}}
	token := strings.Repeat("t", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("doctor request lost authentication or path")
		}
		_ = json.NewEncoder(w).Encode(report)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	path := diagnosticFixture(t, net.JoinHostPort(host, port))
	t.Setenv("DARWIN_API_TOKEN", token)
	var out, stderr bytes.Buffer
	if code := RunWithInput([]string{"doctor", "--config", path}, strings.NewReader(""), &out, &stderr, "test"); code != 0 || stderr.Len() != 0 {
		t.Fatalf("doctor code=%d out=%q err=%q", code, out.String(), stderr.String())
	}
	var got health.Report
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Validate() != nil || !got.Ready {
		t.Fatalf("invalid doctor output: %s", out.String())
	}
}
