package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/traces"
)

func waitTraceExportHealth(t *testing.T, exporter *TraceExporter, status string) {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		check := exporter.Health()
		if check.Status == status {
			if check.Component != "trace_export" || check.Validate() != nil {
				t.Fatal("invalid trace export health", check)
			}
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("trace health did not converge", status, check)
		}
	}
}

func TestTraceExporterDisabledAndInvalidStart(t *testing.T) {
	s := submissionService(t)
	for _, cfg := range []*config.TraceExport{nil, {Enabled: false}} {
		s.settings.Telemetry.TraceExport = cfg
		exporter, err := StartConfiguredTraceExport(context.Background(), s)
		if err != nil || exporter == nil || exporter.Health().Status != "disabled" || exporter.Health().Validate() != nil || exporter.Close() != nil {
			t.Fatal(exporter, err)
		}
	}
	for _, interval := range []time.Duration{0, time.Millisecond, 24*time.Hour + time.Second} {
		if exporter, err := StartTraceExport(context.Background(), s, traces.ExportOptions{Endpoint: "http://127.0.0.1:1/v1/traces"}, interval); err == nil || exporter != nil {
			t.Fatal("invalid interval started", interval)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if exporter, err := StartTraceExport(ctx, s, traces.ExportOptions{Endpoint: "http://127.0.0.1:1/v1/traces"}, time.Second); err == nil || exporter != nil {
		t.Fatal("canceled exporter started")
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled exporter created storage", err)
	}
}

func TestConfiguredTraceExporterDeliversAndReportsHealth(t *testing.T) {
	s := metricsExportFixture(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/traces" || !bytes.Contains(body, []byte("resourceSpans")) {
			t.Error("invalid trace request")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	s.settings.Telemetry.TraceExport = &config.TraceExport{Enabled: true, Endpoint: server.URL + "/v1/traces", Interval: "1s", Limit: 1}
	exporter, err := StartConfiguredTraceExport(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close()
	waitTraceExportHealth(t, exporter, "healthy")
	if calls.Load() != 1 || exporter.Close() != nil || exporter.Health().Status != "unavailable" {
		t.Fatal(calls.Load(), exporter.Health())
	}
}

func TestConfiguredTraceExporterFencesConfigurationRotation(t *testing.T) {
	s := metricsExportFixture(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s.settings.Telemetry.TraceExport = &config.TraceExport{Enabled: true, Endpoint: server.URL + "/v1/traces", APIKeyEnv: "TRACE_KEY", Interval: "1s"}
	s.secret = func(string) string {
		s.settings.Telemetry.TraceExport.Enabled = false
		return "fixture-token"
	}
	exporter, err := StartConfiguredTraceExport(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close()
	waitTraceExportHealth(t, exporter, "degraded")
	if calls.Load() != 0 {
		t.Fatal("rotated configuration dispatched")
	}
}
