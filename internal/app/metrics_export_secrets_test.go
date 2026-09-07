package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

func TestMetricsExportSecretSetAndAbsentLookupCompatibility(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers = []config.Provider{{ID: "provider", APIKeyEnv: "PROVIDER_KEY"}}
	calls := []string{}
	resolve := func(name string) string { calls = append(calls, name); return name + "-value" }
	values := memorySecrets(cfg, resolve)
	if !reflect.DeepEqual(calls, []string{"DARWIN_API_TOKEN", "PROVIDER_KEY"}) || len(values) != 2 {
		t.Fatal(values, calls)
	}
	for _, enabled := range []bool{false, true} {
		cfg.Telemetry.MetricsExport = &config.MetricsExport{Enabled: enabled, APIKeyEnv: "COLLECTOR_KEY"}
		calls = nil
		values = memorySecrets(cfg, resolve)
		if !slices.Contains(values, "COLLECTOR_KEY-value") || !reflect.DeepEqual(calls, []string{"DARWIN_API_TOKEN", "PROVIDER_KEY", "COLLECTOR_KEY"}) {
			t.Fatal(values, calls)
		}
	}
	if metricsExportSecret(cfg, nil) != "" {
		t.Fatal("nil resolver")
	}
	cfg.Telemetry.MetricsExport = &config.MetricsExport{}
	calls = nil
	if metricsExportSecret(cfg, resolve) != "" || len(calls) != 0 {
		t.Fatal(calls)
	}
}

func TestConfiguredSensitiveEnvironmentValueJoinsRuntimeRedaction(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.RedactEnv = []string{"CUSTOM_SENSITIVE_VALUE"}
	const value = "operator-configured-private-value"
	values := memorySecrets(cfg, func(name string) string {
		if name == "CUSTOM_SENSITIVE_VALUE" {
			return value
		}
		return ""
	})
	if got := redact("before "+value+" after", values); got != "before [REDACTED] after" {
		t.Fatal(got)
	}
}

func TestMetricsExportSecretSubmissionRejectsBeforePersistence(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := submissionService(t)
		s.settings.Telemetry.MetricsExport = &config.MetricsExport{Enabled: enabled, Endpoint: "https://collector.example/v1/metrics", APIKeyEnv: "COLLECTOR_KEY"}
		credential := "collector-\"private\ncredential"
		s.secret = func(name string) string {
			if name == "COLLECTOR_KEY" {
				return credential
			}
			return ""
		}
		_, err := s.Submit(context.Background(), "0123456789abcdef", Request{ModelID: "fixture", Prompt: credential})
		if !errors.Is(err, ErrAdmission) {
			t.Fatal(err)
		}
		if _, err = os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("secret submission created storage", err)
		}
	}
}

func TestMetricsExportSecretRunAndHealthRedaction(t *testing.T) {
	const credential = "private-collector-key"
	const providerCredential = "private-provider-key"
	const customSensitive = "private-operator-field"
	requestBody := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		requestBody = string(body)
		if r.Header.Get("Authorization") != "Bearer "+providerCredential {
			t.Error("collector credential used as provider auth")
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", credential+" answer "+customSensitive)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
	cfg.Telemetry.MetricsExport = &config.MetricsExport{APIKeyEnv: "COLLECTOR_KEY"}
	cfg.Security.RedactEnv = []string{"CUSTOM_SENSITIVE_VALUE"}
	cfg.Providers = []config.Provider{{ID: "provider-" + credential, Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "PROVIDER_KEY"}}
	cfg.Models = []config.Model{{ID: "model-" + credential, Provider: cfg.Providers[0].ID, Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	resolve := func(name string) string {
		if name == "PROVIDER_KEY" {
			return providerCredential
		}
		if name == "COLLECTOR_KEY" {
			return credential
		}
		if name == "CUSTOM_SENSITIVE_VALUE" {
			return customSensitive
		}
		return ""
	}
	out, err := RunExplicit(context.Background(), cfg, Request{ModelID: cfg.Models[0].ID, Prompt: "avoid storing " + credential + ", " + providerCredential + " and " + customSensitive}, resolve)
	if err != nil || strings.Contains(out.Text, credential) || out.Text != "[REDACTED] answer [REDACTED]" {
		t.Fatal(out, err)
	}
	// Default assembly receives redacted fresh input, not just a clean journal.
	if strings.Contains(requestBody, credential) || strings.Contains(requestBody, providerCredential) || strings.Contains(requestBody, customSensitive) {
		t.Fatal("collector key escaped in provider request")
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		body, _ := event.Encode()
		if strings.Contains(string(body), credential) || strings.Contains(string(body), providerCredential) || strings.Contains(string(body), customSensitive) {
			t.Fatal("collector key persisted")
		}
	}
	s := &Service{settings: cfg, secret: resolve, profile: healthProfile}
	report, err := s.HealthReport(context.Background(), healthySupervisor())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(report)
	if strings.Contains(string(body), credential) {
		t.Fatal("collector key exposed in health identity")
	}
}

func TestMetricsExportSecretDefaultContextRedactsExactlyOnce(t *testing.T) {
	r := Request{Prompt: "value REDACTED"}
	got, err := prepareTaskContext(context.Background(), &r, []string{"REDACTED"})
	if err != nil || len(got) != 1 || got[0].Content != "value [REDACTED]" || r.Prompt != "value REDACTED" {
		t.Fatal(got, err)
	}
	// Reusing the owned assembly must not replace marker text a second time.
	again, err := prepareTaskContext(context.Background(), &r, []string{"REDACTED"})
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal(again, err)
	}
}
