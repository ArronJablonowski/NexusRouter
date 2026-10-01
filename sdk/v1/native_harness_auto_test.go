package v1_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

func TestSDKNativeAutoLearnsExactPairsAndHonorsBudget(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	var calls atomic.Int32
	var hideBeta atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			if hideBeta.Load() {
				fmt.Fprint(w, `{"models":[{"name":"alpha"}]}`)
			} else {
				fmt.Fprint(w, `{"models":[{"name":"alpha"},{"name":"beta"}]}`)
			}
			return
		}
		calls.Add(1)
		var request struct{ Model string }
		if json.NewDecoder(r.Body).Decode(&request) != nil || (request.Model != "alpha" && request.Model != "beta") {
			t.Error("unregistered model")
			http.Error(w, "bad", 400)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, "{\"model\":%q,\"message\":{\"role\":\"assistant\",\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", request.Model, request.Model+" fixture")
	}))
	defer server.Close()
	executable, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(body)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Routing.Exploration = 0
	cfg.Tools.Enabled = false
	cfg.Hardware.Concurrent = "1"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Providers = []config.Provider{{ID: "native", Kind: "ollama", Endpoint: server.URL, RequestTimeout: "15s"}}
	cheap, expensive := .01, .2
	cfg.Models = []config.Model{
		{ID: "alpha-id", Provider: "native", Model: "alpha", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &cheap},
		{ID: "beta-id", Provider: "native", Model: "beta", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &expensive},
	}
	body, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	options := sdk.ConfigOptions{ProjectFile: file, HarnessEvidence: ledger, ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })}
	for _, name := range []string{"alpha", "beta"} {
		options.NativeHarnesses = append(options.NativeHarnesses, sdk.NativeHarness{ID: "pi-" + name, ModelID: name + "-id", Kind: "pi", Executable: executable, ExecutableSHA256: hex.EncodeToString(pin[:]), ModelRevision: name + "-weights-1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &pi.Prices{}})
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	reviews := map[string]harness.Review{}
	tasks := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		result, e := client.Run(ctx, sdk.Request{Version: 1, HarnessID: "pi-" + name, ModelID: name + "-id", Prompt: "Fixture answer.", Domain: "writing", Profile: "rubric-1", HarnessDifficulty: "hard", ContextTokens: 16384})
		if e != nil || result.Text != name+" fixture" || result.HarnessOutcome == nil || result.HarnessOutcome.Task.Difficulty != "hard" {
			t.Fatal(result, e)
		}
		digest, _ := result.HarnessOutcome.Digest()
		verdict := "failed"
		quality := 0.0
		if name == "beta" {
			verdict = "passed"
			quality = 1
		}
		review := harness.Review{Version: 1, ID: name + "-review", ExecutionDigest: digest, Verdict: verdict, Method: "deterministic", MethodVersion: "test-rubric-1", Reviewer: "fixture-test", Confidence: 1, Quality: quality, CreatedAt: time.Now().UTC()}
		// Controlled fixture labels exercise ranking, not real model quality.
		if e = client.ReviewHarnessOutcome(ctx, ledger, result.TaskID, review); e != nil {
			t.Fatal(e)
		}
		reviews[name] = review
		tasks[name] = result.TaskID
	}
	req := sdk.Request{Version: 1, HarnessID: "auto", ModelID: "auto", Prompt: "Fixture answer.", Domain: "writing", Profile: "rubric-1", HarnessDifficulty: "hard", ContextTokens: 16384, MaxCost: 1}
	result, err := client.Run(ctx, req)
	if err != nil || result.Text != "beta fixture" || result.HarnessSelection == nil || result.HarnessSelection.Validate() != nil || result.HarnessSelection.Primary.ConfirmedSamples != 1 {
		t.Fatal("accuracy did not beat price", result, err)
	}
	page, err := client.ReadEvents(ctx, result.TaskID, 0, 10)
	if err != nil || len(page.Events) != 2 || page.Events[0].Data.Harness.Selection == nil || page.Events[0].Data.Harness.Selection.Primary.Identity != result.HarnessOutcome.Actual {
		t.Fatal("decision not durable", page, err)
	}

	// A top-ranked pair that cannot obtain admission is excluded before inference.
	options.NativeHarnesses[1].OverheadRAMBytes = 1 << 62
	constrained, e := sdk.New(options)
	if e != nil {
		t.Fatal(e)
	}
	constrainedResult, e := constrained.Run(ctx, req)
	if e != nil || constrainedResult.Text != "alpha fixture" || constrainedResult.HarnessSelection == nil || len(constrainedResult.HarnessSelection.Excluded) != 1 || constrainedResult.HarnessSelection.Excluded[0].Reasons[0] != "capacity" {
		t.Fatal("capacity did not rerank before dispatch", constrainedResult, e)
	}
	options.NativeHarnesses[1].OverheadRAMBytes = 64 << 20
	// Availability is refreshed on every automatic request, not reused from the
	// initial explicit execution or the previous inventory response.
	hideBeta.Store(true)
	unavailable, e := client.Run(ctx, req)
	if e != nil || unavailable.Text != "alpha fixture" || len(unavailable.HarnessSelection.Excluded) != 1 || unavailable.HarnessSelection.Excluded[0].Reasons[0] != "unavailable" {
		t.Fatal("stale inventory used", unavailable, e)
	}
	hideBeta.Store(false)
	before := calls.Load()
	req.MaxCost = 0
	if _, err = client.Run(ctx, req); err == nil || calls.Load() != before {
		t.Fatal("strict zero-cost auto budget dispatched", err)
	}
	req.MaxCost = .05
	result, err = client.Run(ctx, req)
	if err != nil || result.Text != "alpha fixture" || len(result.HarnessSelection.Excluded) != 1 {
		t.Fatal("budget ignored", result, err)
	}
	// Revising both labels reverses the best pair, including after SDK restart.
	for _, name := range []string{"alpha", "beta"} {
		review := reviews[name]
		review.ExpectedHead = review.ID
		review.ID += "-revision"
		review.CreatedAt = time.Now().UTC()
		if name == "alpha" {
			review.Verdict = "passed"
			review.Quality = 1
		} else {
			review.Verdict = "failed"
			review.Quality = 0
		}
		if err = client.ReviewHarnessOutcome(ctx, ledger, tasks[name], review); err != nil {
			t.Fatal(err)
		}
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	req.MaxCost = 1
	result, err = client.Run(ctx, req)
	if err != nil || result.Text != "alpha fixture" || result.HarnessSelection.Primary.ConfirmedSamples != 1 {
		t.Fatal("latest review ignored", result, err)
	}
	req.HarnessDifficulty = "easy"
	result, err = client.Run(ctx, req)
	if err != nil || result.HarnessOutcome.Task.Difficulty != "easy" || result.HarnessSelection.Primary.EffectiveSamples != 0 || result.HarnessSelection.Reason != "insufficient_evidence_stable_tiebreak" {
		t.Fatal("borrowed other difficulty", result, err)
	}
	req.HarnessDifficulty = "hard"
	req.HarnessEvaluation = true
	result, err = client.Run(ctx, req)
	if err != nil || result.HarnessSelection.Explored || result.Text != "alpha fixture" {
		t.Fatal("disabled exploration ignored", result, err)
	}
	req.HarnessEvaluation = false
	req.Profile = "unseen-profile"
	result, err = client.Run(ctx, req)
	if err != nil || result.HarnessSelection.Reason != "insufficient_evidence_stable_tiebreak" || result.HarnessSelection.Primary.EffectiveSamples != 0 {
		t.Fatal("borrowed unrelated evidence", result, err)
	}
	if calls.Load() != 10 {
		t.Fatal("duplicate or missing inference", calls.Load())
	}
}
