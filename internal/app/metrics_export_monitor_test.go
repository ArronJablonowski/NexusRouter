package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
)

func waitMetricsExportHealth(t *testing.T, exporter *MetricsExporter, status string) {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		check := exporter.Health()
		if check.Status == status {
			if check.Component != "metrics_export" || check.Validate() != nil {
				t.Fatal("invalid metrics export health", check)
			}
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("metrics health did not converge", status, check)
		}
	}
}

func TestMetricsExporterDisabledAndInvalidStartDoNotDispatch(t *testing.T) {
	s := submissionService(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, cfg := range []*config.MetricsExport{nil, {Enabled: false}} {
		s.settings.Telemetry.MetricsExport = cfg
		exporter, err := StartConfiguredMetricsExport(context.Background(), s)
		if err != nil || exporter == nil {
			t.Fatal("disabled exporter not inert", exporter, err)
		}
		if check := exporter.Health(); check.Component != "metrics_export" || check.Status != "disabled" || check.Code != "disabled_by_policy" || check.Validate() != nil {
			t.Fatal("invalid disabled health", check)
		}
		if err := exporter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, interval := range []time.Duration{0, time.Millisecond, 24*time.Hour + time.Second} {
		if e, err := StartMetricsExport(context.Background(), s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, interval); err == nil || e != nil {
			t.Fatal("invalid interval started", interval, err)
		}
	}
	if e, err := StartMetricsExport(nil, s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, time.Second); err == nil || e != nil {
		t.Fatal("nil context started", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e, err := StartMetricsExport(ctx, s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, time.Second); err == nil || e != nil {
		t.Fatal("canceled context started", err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) || calls.Load() != 0 {
		t.Fatal("disabled/invalid start performed I/O", err, calls.Load())
	}
}

func TestOpenTelemetrySwitchRunsTasksAndConfiguredExporter(t *testing.T) {
	base, cfg := autoFixture(t)
	var calls atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	cfg.Telemetry.OTEL = true
	cfg.Telemetry.MetricsExport = &config.MetricsExport{Endpoint: collector.URL + "/v1/metrics", Interval: "1s"}
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.profile = base.profile
	result, err := s.Run(context.Background(), Request{ModelID: "a", Prompt: "runtime remains available"})
	if err != nil || result.Text != "a" {
		t.Fatal("telemetry switch disabled task execution", result, err)
	}
	exporter, err := StartConfiguredMetricsExport(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close()
	waitMetricsExportHealth(t, exporter, "healthy")
	if calls.Load() != 1 {
		t.Fatal("compatibility switch did not start exporter", calls.Load())
	}
	if err := exporter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsExporterCompletionRelativeCadenceAndClose(t *testing.T) {
	s := metricsExportFixture(t)
	entered := make(chan time.Time, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls, active, maximum atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		a := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); a > old && !maximum.CompareAndSwap(old, a); old = maximum.Load() {
		}
		entered <- time.Now()
		if n == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, err := StartMetricsExport(ctx, s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial export did not start immediately")
	}
	waitMetricsExportHealth(t, e, "unknown")
	// Hold longer than an interval: no overlapping request or catch-up dispatch.
	select {
	case <-entered:
		t.Fatal("overlapping export")
	case <-time.After(1100 * time.Millisecond):
	}
	releasedAt := time.Now()
	releaseOnce.Do(func() { close(release) })
	waitMetricsExportHealth(t, e, "healthy")
	select {
	case started := <-entered:
		if started.Sub(releasedAt) < time.Second {
			t.Fatal("interval measured before prior completion", started.Sub(releasedAt))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second export never started")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	waitMetricsExportHealth(t, e, "unavailable")
	if err := e.Close(); err != nil {
		t.Fatal("idempotent close", err)
	}
	select {
	case <-entered:
		t.Fatal("post-close export")
	case <-time.After(1100 * time.Millisecond):
	}
	if calls.Load() != 2 || maximum.Load() != 1 {
		t.Fatal("unexpected overlap/catch-up", calls.Load(), maximum.Load())
	}
}

func TestMetricsExporterFailureRecoversAndLastErrorIsCleared(t *testing.T) {
	s := metricsExportFixture(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	e, err := StartMetricsExport(context.Background(), s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	waitMetricsExportHealth(t, e, "degraded")
	waitMetricsExportHealth(t, e, "healthy")
	if err := e.Close(); err != nil || calls.Load() != 2 {
		t.Fatal("recovered export retained old failure or retried early", err, calls.Load())
	}
}

func TestMetricsExporterCloseCancelsAndJoinsActiveRequest(t *testing.T) {
	s := metricsExportFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	e, err := StartMetricsExport(context.Background(), s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("collector not reached")
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- e.Close() }()
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("operator cancellation became attempt failure", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Close did not join active attempt")
		}
	}
	waitMetricsExportHealth(t, e, "unavailable")
}

func TestConfiguredMetricsExporterRechecksSettingsAfterCredentialCallback(t *testing.T) {
	for _, mutation := range []string{"disabled", "endpoint", "otel"} {
		t.Run(mutation, func(t *testing.T) {
			s := metricsExportFixture(t)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer server.Close()
			s.settings.Telemetry.MetricsExport = &config.MetricsExport{Enabled: true, Endpoint: server.URL + "/v1/metrics", APIKeyEnv: "DARWIN_TEST_TOKEN", Interval: "1s"}
			if mutation == "otel" {
				s.settings.Telemetry.MetricsExport.Enabled = false
				s.settings.Telemetry.OTEL = true
			}
			s.secret = func(string) string {
				if mutation == "disabled" {
					s.settings.Telemetry.MetricsExport.Enabled = false
				} else if mutation == "endpoint" {
					s.settings.Telemetry.MetricsExport.Endpoint = server.URL + "/changed"
				} else {
					s.settings.Telemetry.OTEL = false
				}
				return "synthetic-monitor-token"
			}
			e, err := StartConfiguredMetricsExport(context.Background(), s)
			if err != nil {
				if calls.Load() != 0 {
					t.Fatal("rejected start dispatched")
				}
				return
			}
			defer e.Close()
			waitMetricsExportHealth(t, e, "degraded")
			if err := e.Close(); !errors.Is(err, metrics.ErrExport) || calls.Load() != 0 {
				t.Fatal("configured authority rotation dispatched or lost failure", err, calls.Load())
			}
		})
	}
}

func TestMetricsExporterEachAttemptReadsFreshDurableSnapshot(t *testing.T) {
	s := metricsExportFixture(t)
	type observation struct{ queued, at string }
	observed := make(chan observation, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ResourceMetrics []struct {
				ScopeMetrics []struct {
					Metrics []struct {
						Name  string
						Gauge struct {
							DataPoints []struct {
								AsInt, TimeUnixNano string
								Attributes          []struct {
									Key   string
									Value struct{ StringValue string }
								}
							}
						}
					}
				}
			}
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&payload); err != nil {
			t.Error("invalid periodic payload", err)
		}
		var item observation
		for _, resource := range payload.ResourceMetrics {
			for _, scope := range resource.ScopeMetrics {
				for _, metric := range scope.Metrics {
					if metric.Name != "darwinrouter.submissions" {
						continue
					}
					for _, point := range metric.Gauge.DataPoints {
						for _, attr := range point.Attributes {
							if attr.Key == "state" && attr.Value.StringValue == "queued" {
								item = observation{point.AsInt, point.TimeUnixNano}
							}
						}
					}
				}
			}
		}
		observed <- item
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	e, err := StartMetricsExport(context.Background(), s, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	next := func() observation {
		t.Helper()
		select {
		case value := <-observed:
			return value
		case <-time.After(3 * time.Second):
			t.Fatal("fresh periodic export not observed")
			return observation{}
		}
	}
	first := next()
	if first.queued != "1" || first.at == "" {
		t.Fatal("first snapshot did not reflect initial database", first)
	}
	waitMetricsExportHealth(t, e, "healthy")
	if _, err := s.Submit(context.Background(), "fedcba9876543210", Request{ModelID: "private-model-identity", Prompt: "second private prompt"}); err != nil {
		t.Fatal(err)
	}
	second := next()
	if second.queued != "2" || second.at == "" || first.at == second.at {
		t.Fatal("periodic exporter reused a prior snapshot", first, second)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}
