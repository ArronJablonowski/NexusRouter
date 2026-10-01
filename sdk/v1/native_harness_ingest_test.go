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
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKNativeCompletionPersistsPendingEvidence(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	for _, mode := range []string{"recorded", "not_configured", "failed"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/x-ndjson")
				fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"fixture answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer provider.Close()
			dir := filepath.Join(t.TempDir(), "evidence")
			ledger, err := harness.OpenEvidenceStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			client, _ := nativeSDKProviderClient(t, provider.URL, 64<<20, false, "ollama", func(c *config.Settings, o *sdk.ConfigOptions) {
				if mode != "not_configured" {
					o.HarnessEvidence = ledger
				}
			})
			if mode == "failed" {
				if err := ledger.Close(); err != nil {
					t.Fatal(err)
				}
			}
			result, err := client.Run(context.Background(), sdk.Request{Version: 1, HarnessID: "pi-fixture", ModelID: "chat", Prompt: "Write an answer.", Domain: "writing", Profile: "fixture-v1"})
			if err != nil || result.Text != "fixture answer" || calls.Load() != 1 || result.HarnessOutcome == nil || result.HarnessEvidenceStatus != mode || result.HarnessReview != nil {
				t.Fatal(result, err, calls.Load())
			}
			if mode != "recorded" {
				repaired, e := harness.OpenEvidenceStore(dir)
				if e != nil {
					t.Fatal(e)
				}
				defer repaired.Close()
				if _, e = client.ReconcileHarnessOutcome(context.Background(), repaired, result.TaskID); e != nil {
					t.Fatal(e)
				}
				ledger = repaired
			}
			now := time.Now().UTC()
			snapshot, err := ledger.Snapshot(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			outcome := *result.HarnessOutcome
			route, err := harness.Select(harness.Request{Version: 1, Task: outcome.Task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: outcome.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 16384}}, snapshot, now, 0)
			if err != nil || route.Primary.PendingOutputs != 1 || route.Primary.ConfirmedSamples != 0 || route.Primary.AdvisorySamples != 0 || route.Primary.EffectiveSamples != 0 {
				t.Fatal("completion fabricated quality", route, err)
			}
			digest, _ := outcome.Digest()
			// Direct ledger append proves the execution already exists without a hidden
			// SDK reconciliation step. The fixture's exact text supplies this review.
			review := harness.Review{Version: 1, ID: "exact-fixture-review", ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "exact-text-v1", Reviewer: "fixture-test", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
			if err := ledger.AppendReview(context.Background(), review, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatal("ledger repair repeated inference")
			}
		})
	}
}
