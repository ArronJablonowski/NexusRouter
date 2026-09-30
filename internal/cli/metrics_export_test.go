package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/metrics"
	"go.yaml.in/yaml/v3"
)

func TestMetricsExportStrictArguments(t *testing.T) {
	base := []string{"--config", "private-config", "--endpoint", "http://127.0.0.1:1/v1/metrics"}
	for _, args := range [][]string{nil, {"--config", ""}, append(append([]string{}, base...), "extra-private"), append(append([]string{}, base...), "--config=x"), append(append([]string{}, base...), "--endpoint=x"), append(append([]string{}, base...), "--api-key-env="), append(append([]string{}, base...), "--api-key-env=A", "--api-key-env=B"), append(append([]string{}, base...), "--api-key=private-token"), {"--config=x", "--endpoint=https://user:private-token@example.com/v1/metrics"}} {
		var out, diagnostic bytes.Buffer
		if code := runMetrics(append([]string{"export"}, args...), &out, &diagnostic); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-") {
			t.Fatal(code, diagnostic.String())
		}
	}
	var out, diagnostic bytes.Buffer
	if code := runMetrics(append([]string{"export"}, base...), &out, &diagnostic); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-") {
		t.Fatal(code, diagnostic.String())
	}
}

func TestMetricsExportConfiguredOneShot(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(dir, "existing.db")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_TEST_METRICS_KEY", "private-test-token")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer private-test-token" || !bytes.Contains(body, []byte("resourceMetrics")) {
			t.Error("invalid collector request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	args := []string{"metrics", "export", "--config=" + path, "--endpoint=" + server.URL + "/v1/metrics", "--api-key-env=DARWIN_TEST_METRICS_KEY"}
	var out, diagnostic bytes.Buffer
	if code := Run(args, &out, &diagnostic, "test"); code != 1 || calls.Load() != 0 {
		t.Fatal("missing database accepted", code, diagnostic.String())
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created database", err)
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfg.Telemetry.Database)
	out.Reset()
	diagnostic.Reset()
	if code := Run(args, &out, &diagnostic, "test"); code != 0 || out.String() != "{\"exported\":true}\n" || diagnostic.Len() != 0 || calls.Load() != 1 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	after, _ := os.ReadFile(cfg.Telemetry.Database)
	if !bytes.Equal(before, after) {
		t.Fatal("database mutated")
	}
}

func TestMetricsExportCanceledBeforeConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "absent.yaml")
	var out, diagnostic bytes.Buffer
	for _, parent := range []context.Context{nil, ctx} {
		if code := runMetricsExportContext(parent, path, metrics.ExportOptions{}, &out, &diagnostic); code != 1 || out.Len() != 0 {
			t.Fatal(code)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created config", err)
	}
}
