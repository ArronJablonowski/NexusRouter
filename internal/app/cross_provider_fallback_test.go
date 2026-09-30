package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// TestCrossProviderFallbackQualification exercises both production wire
// adapters. A discovery-only test cannot prove that a retryable local stream
// failure is journaled before a differently shaped cloud stream is dispatched.
func TestCrossProviderFallbackQualification(t *testing.T) {
	for _, localRequired := range []bool{false, true} {
		t.Run(fmt.Sprintf("local-required-%t", localRequired), func(t *testing.T) {
			const (
				credential = "cross-provider-fixture-secret"
				prompt     = "private cross-provider qualification prompt"
			)
			var localDiscovery, localInference, cloudDiscovery, cloudInference atomic.Int32
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					localDiscovery.Add(1)
					fmt.Fprintln(w, `{"models":[{"name":"a-local"}]}`)
				case "/api/chat":
					localInference.Add(1)
					var request struct {
						Model    string              `json:"model"`
						Messages []providers.Message `json:"messages"`
					}
					if r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "a-local" || len(request.Messages) != 1 || request.Messages[0].Content != prompt {
						t.Error("unexpected Ollama request or credential disclosure")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					t.Errorf("unexpected Ollama path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer local.Close()

			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					cloudDiscovery.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+credential {
						t.Error("cloud discovery credential missing")
					}
					fmt.Fprintln(w, `{"data":[{"id":"z-cloud"}]}`)
				case "/chat/completions":
					cloudInference.Add(1)
					var request struct {
						Model    string              `json:"model"`
						Stream   bool                `json:"stream"`
						Messages []providers.Message `json:"messages"`
					}
					if r.Header.Get("Authorization") != "Bearer "+credential || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "z-cloud" || !request.Stream || len(request.Messages) != 1 || request.Messages[0].Content != prompt {
						t.Error("unexpected OpenAI-compatible request")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cross-provider answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
				default:
					t.Errorf("unexpected OpenAI-compatible path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer cloud.Close()

			cfg := config.Defaults()
			cfg.Mode = "hybrid"
			cfg.Routing.Exploration = 0
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "cross-provider.db")
			cfg.Providers = []config.Provider{
				{ID: "ollama-local", Kind: "ollama", Endpoint: local.URL},
				{ID: "openai-cloud", Kind: "openai_compatible", Endpoint: cloud.URL, APIKeyEnv: "CROSS_PROVIDER_FIXTURE_KEY"},
			}
			localCost, cloudCost := .25, .5
			cfg.Models = []config.Model{
				{ID: "local", Model: "a-local", Provider: "ollama-local", Locality: "local", FailureDomain: "local-host", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &localCost, RAMBytes: 100},
				{ID: "cloud", Model: "z-cloud", Provider: "openai-cloud", Locality: "cloud", FailureDomain: "cloud-service", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &cloudCost},
			}
			svc, err := NewService(cfg, func(name string) string {
				if name == "CROSS_PROVIDER_FIXTURE_KEY" {
					return credential
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}

			out, runErr := svc.Run(context.Background(), Request{Prompt: prompt, Domain: "qualification", MaxCost: localCost + cloudCost, LocalRequired: localRequired})
			if localRequired {
				if runErr == nil || localDiscovery.Load() == 0 || localInference.Load() != 1 || cloudDiscovery.Load() != 0 || cloudInference.Load() != 0 || len(out.PreviousTaskIDs) != 0 || out.RouteEstimatedCost == nil || *out.RouteEstimatedCost != localCost || out.Usage != nil {
					t.Fatalf("local privacy crossed provider boundary: %+v err=%v discoveries=%d/%d inference=%d/%d", out, runErr, localDiscovery.Load(), cloudDiscovery.Load(), localInference.Load(), cloudInference.Load())
				}
				return
			}
			if runErr != nil || out.Text != "cross-provider answer" || localInference.Load() != 1 || cloudInference.Load() != 1 || len(out.PreviousTaskIDs) != 1 || out.RouteEstimatedCost == nil || *out.RouteEstimatedCost != localCost+cloudCost || out.Usage != nil {
				t.Fatalf("cross-provider fallback failed: %+v err=%v inference=%d/%d", out, runErr, localInference.Load(), cloudInference.Load())
			}

			db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			first, err := sessions.Replay(context.Background(), db, out.PreviousTaskIDs[0])
			if err != nil || first.State != "failed" {
				t.Fatalf("local failure history missing: %+v err=%v", first, err)
			}
			final, err := sessions.Replay(context.Background(), db, out.TaskID)
			if err != nil || final.State != "completed" || final.RetryOfTaskID != first.TaskID {
				t.Fatalf("cloud retry lineage missing: %+v err=%v", final, err)
			}
			firstEvents, err := db.Read(context.Background(), first.TaskID, 0, 100)
			if err != nil || len(firstEvents) == 0 || firstEvents[0].Data.RouteEstimatedCost == nil || *firstEvents[0].Data.RouteEstimatedCost != localCost || firstEvents[len(firstEvents)-1].Kind != runtime.TaskFailed || firstEvents[len(firstEvents)-1].Data.Code != "provider_retryable_no_output" {
				t.Fatalf("local failure was not classified safely: events=%v err=%v", firstEvents, err)
			}
			finalEvents, err := db.Read(context.Background(), final.TaskID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if finalEvents[0].Data.RouteEstimatedCost == nil || *finalEvents[0].Data.RouteEstimatedCost != cloudCost {
				t.Fatal("fallback task did not persist its own route estimate")
			}
			attributed := false
			for _, event := range finalEvents {
				if event.Kind != runtime.RouteSelected {
					continue
				}
				raw, encodeErr := event.Encode()
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				if strings.Contains(string(raw), prompt) || strings.Contains(string(raw), credential) || strings.Contains(string(raw), cloud.URL) {
					t.Fatalf("route explanation disclosed sensitive request data: %s", raw)
				}
				attributed = event.Data.Route != nil && event.Data.Route.Primary.Model == "z-cloud" && event.Data.Route.Primary.Provider == "openai-cloud"
			}
			if !attributed {
				t.Fatal("final route did not identify the OpenAI-compatible fallback")
			}
		})
	}
}
