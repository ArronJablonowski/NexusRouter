package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/metrics"
)

func metricsExportFixture(t *testing.T) *Service {
	t.Helper()
	s := submissionService(t)
	s.settings.Mode = "local_only"
	s.secret = func(string) string { return "" }
	if _, err := s.Submit(context.Background(), "0123456789abcdef", Request{ModelID: "private-model-identity", Prompt: "private-prompt-content"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMetricsExportContentFreeOwnedSQLite(t *testing.T) {
	s := metricsExportFixture(t)
	const token = "synthetic-metrics-export-token"
	s.secret = func(name string) string {
		if name != "DARWIN_TEST_METRICS_TOKEN" {
			t.Error("unexpected credential lookup", name)
		}
		return token
	}
	before, err := os.ReadFile(s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/v1/metrics" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect collector request shape")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || !json.Valid(body) || !bytes.Contains(body, []byte("resourceMetrics")) {
			t.Error("missing valid OTLP JSON")
		}
		var payload struct {
			ResourceMetrics []struct {
				ScopeMetrics []struct {
					Metrics []struct {
						Name  string `json:"name"`
						Gauge struct {
							DataPoints []struct {
								AsInt string `json:"asInt"`
							} `json:"dataPoints"`
						} `json:"gauge"`
					} `json:"metrics"`
				} `json:"scopeMetrics"`
			} `json:"resourceMetrics"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error("invalid collector body", err)
		}
		queued := false
		for _, resource := range payload.ResourceMetrics {
			for _, scope := range resource.ScopeMetrics {
				for _, metric := range scope.Metrics {
					if metric.Name == "nexusrouter.submissions" && len(metric.Gauge.DataPoints) == 5 && metric.Gauge.DataPoints[0].AsInt == "1" {
						queued = true
					}
				}
			}
		}
		if !queued {
			t.Error("actual durable queued submission missing from export")
		}
		for _, private := range []string{token, "private-model-identity", "private-prompt-content", "0123456789abcdef", s.settings.Telemetry.Database} {
			if bytes.Contains(body, []byte(private)) {
				t.Error("private source data entered metrics payload")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	if err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics", APIKeyEnv: "DARWIN_TEST_METRICS_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.settings.Telemetry.Database)
	if err != nil || !bytes.Equal(before, after) || calls.Load() != 1 {
		t.Fatal("export mutated database or did not send exactly once", err, calls.Load())
	}
}

func TestMetricsExportAdmissionDoesNotCreateDatabaseOrSend(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s := submissionService(t)
	s.settings.Mode = "local_only"
	options := metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}
	if err := s.ExportMetrics(context.Background(), options); err == nil {
		t.Fatal("missing database exported")
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing database created", err)
	}
	s = metricsExportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ExportMetrics(ctx, options); err == nil {
		t.Fatal("canceled export accepted")
	}
	if err := s.ExportMetrics(nil, options); err == nil {
		t.Fatal("nil context accepted")
	}
	var absent *Service
	if err := absent.ExportMetrics(context.Background(), options); err == nil {
		t.Fatal("nil service accepted")
	}
	options.APIKeyEnv = "DARWIN_TEST_MISSING_METRICS_TOKEN"
	if err := s.ExportMetrics(context.Background(), options); err == nil {
		t.Fatal("missing requested credential accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("denied export reached collector", calls.Load())
	}
}

func TestMetricsExportCollectorResponseAndNoRetry(t *testing.T) {
	for _, tc := range []struct {
		name, media, body string
		status            int
		ok                bool
	}{
		{"empty", "application/json", `{}`, 200, true},
		{"zero_rejected", "application/json", `{"partialSuccess":{"rejectedDataPoints":"0"}}`, 200, true},
		{"warning", "application/json", `{"partialSuccess":{"rejectedDataPoints":"0","errorMessage":"private-response-body"}}`, 200, true},
		{"future_field", "application/json", `{"future":{"value":true}}`, 200, true},
		{"rejected", "application/json", `{"partialSuccess":{"rejectedDataPoints":"1","errorMessage":"private-response-body"}}`, 200, false},
		{"rate_limit", "application/json", `{"error":"private-response-body"}`, 429, false},
		{"server_error", "application/json", `{"error":"private-response-body"}`, 503, false},
		{"no_content", "application/json", "", 204, false},
		{"wrong_media", "text/plain", `{}`, 200, false},
		{"malformed", "application/json", `{"private-response-body":`, 200, false},
		{"trailing", "application/json", `{} {}`, 200, false},
		{"oversize", "application/json", `{}` + strings.Repeat(" ", 64<<10), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := metricsExportFixture(t)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.media)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"})
			if (err == nil) != tc.ok || calls.Load() != 1 {
				t.Fatal("collector acceptance or retry mismatch", err, calls.Load())
			}
			if err != nil && (strings.Contains(err.Error(), "private-response-body") || strings.Contains(err.Error(), server.URL)) {
				t.Fatal("raw collector detail escaped")
			}
		})
	}
}

func TestMetricsExportRedirectNeverHandsOff(t *testing.T) {
	s := metricsExportFixture(t)
	var first, second atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { second.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		http.Redirect(w, r, target.URL+"/v1/metrics", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	if err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}); err == nil {
		t.Fatal("redirect accepted")
	}
	if first.Load() != 1 || second.Load() != 0 {
		t.Fatal("redirect followed", first.Load(), second.Load())
	}
}

func TestMetricsExportNetworkPolicyRejectsRemote(t *testing.T) {
	s := metricsExportFixture(t)
	for _, endpoint := range []string{"http://192.0.2.1/v1/metrics", "https://192.0.2.1/v1/metrics"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.ExportMetrics(ctx, metrics.ExportOptions{Endpoint: endpoint})
		if err == nil || ctx.Err() != nil || strings.Contains(err.Error(), endpoint) {
			t.Fatal("local policy failed to deny remote export immediately and generically", err)
		}
		cancel()
	}
	s.settings.Mode = "hybrid"
	if err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: "http://192.0.2.1/v1/metrics"}); err == nil {
		t.Fatal("external cleartext collector accepted")
	}
}

func TestMetricsExportCancellationDuringCollectorResponse(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ExportMetrics(ctx, metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics"}) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("export failed before reaching owned collector", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("collector was not reached")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), server.URL) {
			t.Fatal("cancellation did not return generic failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled export did not join")
	}
}

func TestMetricsExportInvalidCredentialsNeverSend(t *testing.T) {
	s := metricsExportFixture(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, token := range []string{"", "private\nheader-injection", "private\x00token", "private-é-token", strings.Repeat("x", (8<<10)+1)} {
		s.secret = func(string) string { return token }
		err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics", APIKeyEnv: "DARWIN_TEST_TOKEN"})
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid credential accepted or disclosed", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid credential dispatched", calls.Load())
	}
}

func TestMetricsExportFinalCredentialLookupCannotRotateAuthority(t *testing.T) {
	for _, mutation := range []string{"mode", "database", "otel", "credential", "panic"} {
		t.Run(mutation, func(t *testing.T) {
			s := metricsExportFixture(t)
			s.settings.Mode = "hybrid"
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{}`)
			}))
			defer server.Close()
			lookups := 0
			s.secret = func(string) string {
				lookups++
				if lookups == 2 {
					switch mutation {
					case "mode":
						s.settings.Mode = "local_only"
					case "database":
						s.settings.Telemetry.Database += ".rotated"
					case "otel":
						s.settings.Telemetry.OTEL = true
					case "credential":
						return "synthetic-rotated-token"
					case "panic":
						panic("private-credential-panic")
					}
				}
				return "synthetic-original-token"
			}
			err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: server.URL + "/v1/metrics", APIKeyEnv: "DARWIN_TEST_TOKEN"})
			if err == nil || !errors.Is(err, metrics.ErrExport) || calls.Load() != 0 {
				t.Fatal("changed export authority accepted", err, calls.Load())
			}
		})
	}
}
