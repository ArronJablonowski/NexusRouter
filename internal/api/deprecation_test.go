package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

const deprecationBody = `{"version":1,"model_id":"model","domain":"code","profile":"default","policy":{"window":50,"min_samples":20,"failure_threshold":0.35}}`

func deprecationReport() evaluation.DeprecationReport {
	r, _ := evaluation.SummarizeDeprecation(nil, evaluation.DeprecationPolicy{Window: 50, MinSamples: 20, FailureThreshold: .35})
	r.Key = routing.Key{Model: "gemma4:12b", Provider: "local", Domain: "code", Profile: "default"}
	r.ConfiguredModelID = "model"
	return r
}

func TestModelDeprecationMetadataOnly(t *testing.T) {
	s := services()
	calls := 0
	s.ModelDeprecation = func(ctx context.Context, model, domain, profile string, p evaluation.DeprecationPolicy) (evaluation.DeprecationReport, error) {
		calls++
		if model != "model" || domain != "code" || profile != "default" || p != deprecationReport().Policy {
			t.Fatal("wrong attribution")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Fatal("deadline")
		}
		return deprecationReport(), nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/models/deprecation", deprecationBody))
	var got evaluation.DeprecationReport
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != deprecationReport() || calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
}

func TestModelDeprecationAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "query", "forcequery", "method", "media", "missing_hook", "capacity", "canceled", "oversize", "transfer", "unknown_length", "missing", "unknown", "alias", "duplicate", "escaped_duplicate", "policy_duplicate", "policy_escaped_duplicate", "policy_unknown", "policy_missing", "policy_null", "version_null", "version_string", "version_fraction", "window_fraction", "window_zero", "window_large", "minimum_zero", "minimum_large", "threshold_zero", "threshold_large", "threshold_string", "threshold_overflow", "trailing", "utf8"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.ModelDeprecation = func(context.Context, string, string, string, evaluation.DeprecationPolicy) (evaluation.DeprecationReport, error) {
				calls++
				return deprecationReport(), nil
			}
			if mode == "missing_hook" {
				s.ModelDeprecation = nil
			}
			h, _ := New(token, 1, s)
			body := deprecationBody
			want := 400
			switch mode {
			case "missing":
				body = `{}`
			case "unknown":
				body = strings.Replace(body, `"version":1`, `"other":1,"version":1`, 1)
			case "alias":
				body = strings.Replace(body, `"model_id"`, `"Model_ID"`, 1)
			case "duplicate":
				body = strings.Replace(body, `"version":1`, `"version":1,"version":1`, 1)
			case "escaped_duplicate":
				body = strings.Replace(body, `"version":1`, `"version":1,"\u0076ersion":1`, 1)
			case "policy_duplicate":
				body = strings.Replace(body, `"window":50`, `"window":50,"window":50`, 1)
			case "policy_escaped_duplicate":
				body = strings.Replace(body, `"window":50`, `"window":50,"\u0077indow":50`, 1)
			case "policy_unknown":
				body = strings.Replace(body, `"window":50`, `"extra":1,"window":50`, 1)
			case "policy_missing":
				body = strings.Replace(body, `"min_samples":20,`, "", 1)
			case "policy_null":
				body = strings.Replace(body, `{"window":50,"min_samples":20,"failure_threshold":0.35}`, `null`, 1)
			case "version_null":
				body = strings.Replace(body, `"version":1`, `"version":null`, 1)
			case "version_string":
				body = strings.Replace(body, `"version":1`, `"version":"1"`, 1)
			case "version_fraction":
				body = strings.Replace(body, `"version":1`, `"version":1.5`, 1)
			case "window_fraction":
				body = strings.Replace(body, `"window":50`, `"window":50.5`, 1)
			case "window_zero":
				body = strings.Replace(body, `"window":50`, `"window":0`, 1)
			case "window_large":
				body = strings.Replace(body, `"window":50`, `"window":1001`, 1)
			case "minimum_zero":
				body = strings.Replace(body, `"min_samples":20`, `"min_samples":0`, 1)
			case "minimum_large":
				body = strings.Replace(body, `"min_samples":20`, `"min_samples":51`, 1)
			case "threshold_zero":
				body = strings.Replace(body, `0.35`, `0`, 1)
			case "threshold_large":
				body = strings.Replace(body, `0.35`, `1.1`, 1)
			case "threshold_string":
				body = strings.Replace(body, `0.35`, `"0.35"`, 1)
			case "threshold_overflow":
				body = strings.Replace(body, `0.35`, `1e1000`, 1)
			case "trailing":
				body += ` {}`
			case "utf8":
				body += string([]byte{255})
			case "oversize":
				body = strings.Repeat("x", 4097)
				want = 413
			}
			r := request("POST", "/v1/models/deprecation", body)
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "x=y"
			case "forcequery":
				r.URL.ForceQuery = true
			case "method":
				r.Method = "GET"
				want = 405
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = 415
			case "missing_hook":
				want = 503
			case "capacity":
				h.deprecationSlots <- struct{}{}
				want = 503
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = 503
			case "transfer":
				r.TransferEncoding = []string{"chunked"}
			case "unknown_length":
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestModelDeprecationBackendValidation(t *testing.T) {
	for _, mode := range []string{"error", "panic", "version", "policy", "population", "approval", "count", "reason", "digest", "canceled", "redacted_identity"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := services()
			s.ModelDeprecation = func(context.Context, string, string, string, evaluation.DeprecationPolicy) (evaluation.DeprecationReport, error) {
				out := deprecationReport()
				switch mode {
				case "error":
					return out, errors.New("PRIVATE_BACKEND")
				case "panic":
					panic("PRIVATE_BACKEND")
				case "version":
					out.Version = 2
				case "policy":
					out.Policy.Window = 51
				case "population":
					out.Population = "PRIVATE_BACKEND"
				case "approval":
					out.ApprovalRequired = false
				case "count":
					out.Failures = 1
				case "reason":
					out.Reason = "PRIVATE_BACKEND"
				case "digest":
					out.EvidenceDigest = "bad"
				case "canceled":
					cancel()
				case "redacted_identity":
					out.ConfiguredModelID = "[REDACTED]"
					out.Key.Model = "[REDACTED]"
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/models/deprecation", deprecationBody).WithContext(ctx))
			want := 503
			if mode == "redacted_identity" {
				want = 200
			}
			if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE_BACKEND") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestModelDeprecationHTTPServiceNoInference(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "report.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "model", Model: "gemma4:12b", Provider: "local", Locality: "local", ContextTokens: 4096, RAMBytes: 1, Capabilities: []string{"code"}}}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.ModelDeprecation = svc.ModelDeprecation
	h, _ := New(token, 1, s)
	server := httptest.NewServer(h)
	defer server.Close()
	r, err := http.NewRequest("POST", server.URL+"/v1/models/deprecation", strings.NewReader(deprecationBody))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var report evaluation.DeprecationReport
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&report) != nil || report.Sampled != 0 || report.Candidate || !report.ApprovalRequired || report.Key.Model != "gemma4:12b" || calls.Load() != 0 {
		t.Fatal(resp.StatusCode, report, calls.Load())
	}
}
