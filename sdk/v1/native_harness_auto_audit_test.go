package v1_test

import (
	"context"
	"errors"
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

func TestSDKNativeAutomaticAuditFeedsAdvisoryEvidence(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	nativeAutomaticAudit(t, "pi")
}

func TestSDKOpenClawAutomaticAuditFeedsAdvisoryEvidence(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_NATIVE") != "1" {
		t.Skip("requires installed OpenClaw")
	}
	nativeAutomaticAudit(t, "openclaw")
}

func nativeAutomaticAudit(t *testing.T, kind string) {
	registration := nativeRegistration(t, kind)
	for _, verdict := range []string{"accept", "reject", "abstain", "failure"} {
		t.Run(verdict, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/x-ndjson")
				fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"candidate answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			evaluator := validSDKEvaluator()
			evaluator.response.Audit.Domain = "writing"
			if verdict == "failure" {
				evaluator.call = func(context.Context, sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
					return sdk.EvaluatorResponse{}, errors.New("private error")
				}
			} else {
				evaluator.response.Audit.Verdict = verdict
				if verdict == "abstain" {
					evaluator.response.Audit.Confidence = 0
					evaluator.response.Audit.Findings = []sdk.AuditFinding{}
				}
			}
			client, _ := nativeSDKProviderClient(t, server.URL, 64<<20, false, "ollama", func(cfg *config.Settings, options *sdk.ConfigOptions) {
				cfg.Providers[0].RequestTimeout = "30s"
				options.NativeHarnesses = []sdk.NativeHarness{registration}
				cfg.Evaluation.Judge = true
				cfg.Evaluation.AutoReviewModel = "reviewer"
				reviewer := cfg.Models[0]
				reviewer.ID = "reviewer"
				reviewer.Model = "reviewer"
				cfg.Models = append(cfg.Models, reviewer)
				options.Evaluator = evaluator
				options.HarnessEvidence = ledger
			})
			result, err := client.Run(context.Background(), sdk.Request{Version: 1, HarnessID: registration.ID, ModelID: "chat", Prompt: "Write an answer.", Domain: "writing"})
			if err != nil || result.Text != "candidate answer" || result.HarnessOutcome == nil || result.HarnessOutcome.Actual.Harness != kind || calls.Load() != 1 || evaluator.calls.Load() != 1 {
				t.Fatal("candidate rerun or review corrupted execution", result, err)
			}
			if verdict == "failure" {
				if result.HarnessReview != nil || result.HarnessReviewStatus != "failed" || result.AuditStatus != "failed" {
					t.Fatal(result)
				}
				if _, err = client.ReconcileHarnessAudit(context.Background(), ledger, result.TaskID, result.HarnessAuditOperationID); err == nil {
					t.Fatal("failed audit became evidence")
				}
			} else {
				if result.HarnessReview == nil || result.HarnessReviewStatus != "recorded" {
					t.Fatal("automatic feedback absent", result)
				}
				replay, e := client.ReconcileHarnessAudit(context.Background(), ledger, result.TaskID, result.HarnessAuditOperationID)
				if e != nil || replay != *result.HarnessReview {
					t.Fatal(replay, e)
				}
				if verdict != "abstain" && replay.Method != "automated_ai" {
					t.Fatal("advisory promoted to human/deterministic", replay)
				}
			}
			now := time.Now().UTC()
			snapshot, e := ledger.Snapshot(context.Background(), now)
			if e != nil {
				t.Fatal(e)
			}
			selected, e := harness.Select(harness.Request{Version: 1, Task: result.HarnessOutcome.Task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: result.HarnessOutcome.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 16384}}, snapshot, now, 0)
			if e != nil {
				t.Fatal(e)
			}
			rank := selected.Primary
			if rank.ConfirmedSamples != 0 || rank.EffectiveSamples > .25 {
				t.Fatal("automated evidence over-weighted", rank)
			}
			if (verdict == "accept" || verdict == "reject") && rank.AdvisorySamples != 1 {
				t.Fatal("review not learned", rank)
			}
			if (verdict == "failure" || verdict == "abstain") && rank.EffectiveSamples != 0 {
				t.Fatal("non-verdict fabricated quality", rank)
			}
			if evaluator.calls.Load() != 1 || calls.Load() != 1 {
				t.Fatal("reconciliation invoked execution")
			}
		})
	}
}
