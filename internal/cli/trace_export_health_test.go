package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
)

func TestTraceExportHealthIsSupplemental(t *testing.T) {
	report := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: []health.Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "local", Status: "healthy", Code: "available"}, {Component: "metrics_export", Status: "disabled", Code: "disabled_by_policy"}}}
	report.Status, report.Ready = health.Outcome(report.Checks)
	for _, check := range []health.Check{{Component: "trace_export", Status: "unknown", Code: "supervisor_starting"}, {Component: "trace_export", Status: "degraded", Code: "supervisor_error"}, {Component: "trace_export", Status: "disabled", Code: "disabled_by_policy"}, {Component: "trace_export", Status: "unavailable", Code: "supervisor_stopped"}} {
		out, err := withTraceExportHealth(report, check)
		if err != nil || !out.Ready || !report.Ready || len(report.Checks) != 6 {
			t.Fatal(out, err)
		}
		if traceExportDegraded(check) && out.Status != "degraded" {
			t.Fatal("trace failure hidden", out)
		}
		if _, err := withTraceExportHealth(out, check); err == nil {
			t.Fatal("duplicate trace health admitted")
		}
	}
}

func TestServeTraceExportInvalidPreflight(t *testing.T) {
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	path, database := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "absent.db")
	body := fmt.Sprintf("mode: local_only\ndaemon:\n  listen: %q\ntelemetry:\n  database: %q\n  trace_export:\n    enabled: true\n    endpoint: https://remote.invalid/private-path\n    interval: 1s\n", address, database)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_API_TOKEN", strings.Repeat("test-token", 4))
	var out, diagnostic bytes.Buffer
	if code := runServe([]string{"--config", path}, &out, &diagnostic); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-path") {
		t.Fatal(code, diagnostic.String())
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("created database", err)
	}
	listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatal("listener retained", err)
	}
	_ = listener.Close()
}
