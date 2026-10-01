package v1_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKNativeHarnessQueueDurableAuthority(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	nativeQueueAuthority(t, "pi")
}

func TestSDKAdditionalNativeHarnessQueueAuthority(t *testing.T) {
	for _, pair := range []struct{ kind, env string }{{"openclaw", "NEXUS_OPENCLAW_NATIVE"}, {"hermes", "NEXUS_HERMES_NATIVE"}, {"goose", "NEXUS_GOOSE_NATIVE"}, {"openhands", "NEXUS_OPENHANDS_PYTHON"}} {
		t.Run(pair.kind, func(t *testing.T) {
			if os.Getenv(pair.env) == "" {
				t.Skip("requires installed native qualification")
			}
			nativeQueueAuthority(t, pair.kind)
		})
	}
}

func nativeQueueAuthority(t *testing.T, kind string) {
	for _, mode := range []string{"complete", "changed_registration", "cancel_queued", "cancel_running"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var calls atomic.Int32
			entered := make(chan struct{}, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/tags" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
					return
				}
				calls.Add(1)
				if mode == "cancel_running" {
					w.Header().Set("Content-Type", "application/x-ndjson")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					entered <- struct{}{}
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/x-ndjson")
				fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"queued native answer"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":4}`)
			}))
			defer provider.Close()
			registrations := []sdk.NativeHarness{nativeRegistration(t, kind)}
			ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			var cfg config.Settings
			client, _ := nativeSDKProviderClient(t, provider.URL, 64<<20, false, "ollama", func(s *config.Settings, o *sdk.ConfigOptions) {
				cfg = *s
				o.NativeHarnesses = registrations
				o.HarnessEvidence = ledger
			})
			req := sdk.Request{Version: 1, HarnessID: registrations[0].ID, ModelID: "chat", Prompt: "Write an answer.", Domain: "writing", Profile: "queue-v1"}
			queued, err := client.Submit(ctx, "native-queue-authority-key", req)
			if err != nil || queued.State != "queued" {
				t.Fatal(queued, err)
			}
			duplicate, err := client.Submit(ctx, "native-queue-authority-key", req)
			if err != nil || duplicate.ID != queued.ID || calls.Load() != 0 {
				t.Fatal("duplicate admission", duplicate, err)
			}
			if mode == "cancel_queued" {
				if _, err = client.CancelSubmission(ctx, queued.ID); err != nil {
					t.Fatal(err)
				}
			}
			profiler := sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })
			service, err := app.NewServiceWithProfiler(cfg, func(name string) string {
				if name == "NATIVE_TEST_SECRET" {
					return "native-fixture-secret"
				}
				return ""
			}, profiler)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed_registration" {
				registrations[0].ModelRevision = "different-weights"
			}
			if err = service.ConfigureNativeHarnesses(registrations, ledger); err != nil {
				t.Fatal(err)
			}
			dispatcher, err := app.StartDispatcher(ctx, service)
			if err != nil {
				t.Fatal(err)
			}
			defer dispatcher.Close()
			if mode == "cancel_running" {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("provider never started")
				}
				if _, err = client.CancelSubmission(ctx, queued.ID); err != nil {
					t.Fatal(err)
				}
			}
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				status, e := client.SubmissionStatus(ctx, queued.ID)
				if e != nil {
					t.Fatal(e)
				}
				if status.State != "queued" && status.State != "running" {
					if mode == "complete" {
						if status.State != "succeeded" || status.Result == nil || status.Result.Text != "queued native answer" || len(status.TaskIDs) != 1 || calls.Load() != 1 || status.Result.Usage == nil || status.Result.Usage.InputTokens != 10 {
							t.Fatal(status, e, calls.Load())
						}
						outcome, e := client.ReconcileHarnessOutcome(ctx, ledger, status.Result.TaskID)
						if e != nil || outcome.Actual.Model != "fixture" {
							t.Fatal(outcome, e)
						}
						again, e := client.Submit(ctx, "native-queue-authority-key", req)
						if e != nil || again.ID != queued.ID || calls.Load() != 1 {
							t.Fatal("terminal replay", again, e)
						}
						digest, _ := outcome.Digest()
						review := harness.Review{Version: 1, ID: "queue-fixture-exact", ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "fixture-text-v1", Reviewer: "queue-fixture-test", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
						if err = client.ReviewHarnessOutcome(ctx, ledger, status.Result.TaskID, review); err != nil {
							t.Fatal(err)
						}
						auto := req
						auto.HarnessID = "auto"
						auto.ModelID = "auto"
						auto.ContextTokens = 16384
						selected, e := client.Submit(ctx, "native-auto-queue-key", auto)
						for e == nil && (selected.State == "queued" || selected.State == "running") {
							select {
							case <-ctx.Done():
								t.Fatal("automatic queue timed out")
							case <-ticker.C:
							}
							selected, e = client.SubmissionStatus(ctx, selected.ID)
						}
						if e != nil || selected.State != "succeeded" || selected.Result == nil || selected.Result.Text != "queued native answer" || calls.Load() != 2 {
							t.Fatal("reviewed automatic queue", selected, e, calls.Load())
						}

					} else if mode == "cancel_running" {
						if status.State != "canceled" || calls.Load() != 1 || len(status.TaskIDs) != 1 {
							t.Fatal(status, calls.Load())
						}
					} else if calls.Load() != 0 || len(status.TaskIDs) != 0 || (mode == "changed_registration" && status.ErrorCode != "configuration_changed") || (mode == "cancel_queued" && status.State != "canceled") {
						t.Fatal(status, calls.Load())
					}
					return
				}
				select {
				case <-ctx.Done():
					t.Fatal("queue did not settle", status)
				case <-ticker.C:
				}
			}
		})
	}
}
