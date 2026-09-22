package webuiapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func inspectionHandlerFixture(t *testing.T) (*Handler, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	handler := mutationHandlerFixture(t, MutationServices{})
	handler.inspections = InspectionServices{
		Models: func(context.Context) (contract.ModelInspectionPage, error) {
			calls.Add(1)
			return contract.ModelInspectionPage{Version: 1, Availability: contract.Unavailable, LocalProviders: []contract.LocalProviderInspection{}, Models: []contract.ModelInspection{}, Fitness: []contract.ModelFitnessInspection{}}, nil
		},
		Route: func(_ context.Context, task string) (contract.RouteInspection, error) {
			calls.Add(1)
			return contract.RouteInspection{Version: 1, TaskID: task, Availability: contract.Unavailable, Candidates: []contract.RouteCandidateInspection{}}, nil
		},
		Usage: func(_ context.Context, task string) (contract.TaskUsageInspection, error) {
			calls.Add(1)
			return contract.TaskUsageInspection{Version: 1, TaskID: task, Availability: contract.Unavailable}, nil
		},
		Tools: func(_ context.Context, task, after string, limit int) (contract.ToolInspectionPage, error) {
			calls.Add(1)
			if after != "1" || limit != 1 {
				t.Fatalf("unexpected tool query %q %d", after, limit)
			}
			return contract.ToolInspectionPage{Version: 1, TaskID: task, Tools: []contract.ToolInspection{}}, nil
		},
		Audits: func(_ context.Context, task, after string, limit int) (contract.AuditInspectionPage, error) {
			calls.Add(1)
			if after != "" || limit != 25 {
				t.Fatalf("unexpected audit query %q %d", after, limit)
			}
			return contract.AuditInspectionPage{Version: 1, TaskID: task, Audits: []contract.AuditInspection{}}, nil
		},
		Health: func(context.Context) (contract.HealthInspection, error) {
			calls.Add(1)
			return contract.HealthInspection{Version: 1, Availability: contract.Unavailable, Status: "unavailable", Checks: []contract.HealthCheckInspection{}}, nil
		},
		Resources: func(context.Context) (contract.ResourceInspection, error) {
			calls.Add(1)
			return contract.ResourceInspection{Version: 1, Availability: contract.Unavailable}, nil
		},
		Settings: func(context.Context) (contract.SettingsInspection, error) {
			calls.Add(1)
			return contract.SettingsInspection{Version: 1, Digest: strings.Repeat("a", 64), Active: contract.ToolAccessSettings{}, Saved: contract.ToolAccessSettings{}}, nil
		},
	}
	return handler, calls
}

func TestBrowserDeprecationInspectionIsAuthenticatedAndReadOnly(t *testing.T) {
	handler, calls := inspectionHandlerFixture(t)
	handler.inspections.Deprecation = func(_ context.Context, model, domain, profile string, policy evaluation.DeprecationPolicy) (evaluation.DeprecationReport, error) {
		calls.Add(1)
		if model != "coder" || domain != "coding" || profile != "default" || policy.Window != 100 || policy.MinSamples != 20 || policy.FailureThreshold != .35 {
			t.Fatalf("unexpected deprecation request: %q %q %q %+v", model, domain, profile, policy)
		}
		return evaluation.DeprecationReport{Population: "evaluated_attempts", Version: 1, Key: routing.Key{Model: "implementation", Provider: "provider", Domain: domain, Profile: profile}, Policy: policy, ConfiguredModelID: model, Sampled: 20, EligibleSamples: 20, ExecutionFailures: 8, Failures: 8, FailureRate: .4, Candidate: true, Reason: "failure_threshold", ApprovalRequired: true, EvidenceDigest: strings.Repeat("a", 64)}, nil
	}
	target := "/app/api/v1/models/deprecation?model=coder&domain=coding&profile=default&window=100&min_samples=20&failure_threshold=0.35"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, browserGET(target, nil))
	if response.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatal("unauthenticated deprecation inspection dispatched", response.Code, calls.Load())
	}
	cookie, _ := authenticateBrowser(t, handler)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, browserGET(target, cookie))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"candidate":true`) || calls.Load() != 1 {
		t.Fatal("deprecation inspection failed", response.Code, response.Body.String(), calls.Load())
	}
	for _, invalid := range []string{target + "&extra=x", strings.Replace(target, "min_samples=20", "min_samples=0", 1), strings.Replace(target, "model=coder", "model=bad%20id", 1)} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, browserGET(invalid, cookie))
		if response.Code != http.StatusBadRequest {
			t.Fatal("invalid deprecation query accepted", invalid, response.Code)
		}
	}
}

func TestInspectionRoutesRequireAuthenticationAndStrictGET(t *testing.T) {
	handler, calls := inspectionHandlerFixture(t)
	for _, target := range []string{
		"/app/api/v1/models", "/app/api/v1/tasks/task/route", "/app/api/v1/tasks/task/usage",
		"/app/api/v1/tasks/task/tools?after=1&limit=1", "/app/api/v1/tasks/task/audits", "/app/api/v1/health", "/app/api/v1/resources", "/app/api/v1/settings",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserGET(target, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated inspection %s returned %d", target, response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthenticated callbacks invoked", calls.Load())
	}
	cookie, _ := authenticateBrowser(t, handler)
	for _, target := range []string{
		"/app/api/v1/models", "/app/api/v1/tasks/task/route", "/app/api/v1/tasks/task/usage",
		"/app/api/v1/tasks/task/tools?after=1&limit=1", "/app/api/v1/tasks/task/audits", "/app/api/v1/health", "/app/api/v1/resources", "/app/api/v1/settings",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserGET(target, cookie))
		if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
			t.Fatalf("inspection %s returned %d %s", target, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 8 {
		t.Fatal("missing inspection callback", calls.Load())
	}

	for name, mutate := range map[string]func(*http.Request){
		"body":   func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("")) },
		"method": func(r *http.Request) { r.Method = http.MethodHead },
		"query":  func(r *http.Request) { r.URL.ForceQuery = true },
	} {
		t.Run(name, func(t *testing.T) {
			request := browserGET("/app/api/v1/models", cookie)
			mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 {
				t.Fatal("non-canonical inspection GET accepted", response.Code)
			}
		})
	}
}

func TestInspectionQueryAuthenticationPrecedesParsing(t *testing.T) {
	handler, calls := inspectionHandlerFixture(t)
	for _, target := range []string{
		"/app/api/v1/tasks/task/tools?limit=0", "/app/api/v1/tasks/task/audits?after=a&after=b",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserGET(target, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatal("query parsed before authentication", target, response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid unauthorized query dispatched")
	}
	cookie, _ := authenticateBrowser(t, handler)
	for _, target := range []string{
		"/app/api/v1/tasks/task/tools?limit=0", "/app/api/v1/tasks/task/audits?after=a&after=b", "/app/api/v1/tasks/task/tools?unknown=x",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, browserGET(target, cookie))
		if response.Code != http.StatusBadRequest {
			t.Fatal("invalid authenticated query accepted", target, response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid query dispatched")
	}
}
