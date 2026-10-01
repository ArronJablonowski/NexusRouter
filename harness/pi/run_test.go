package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativePiIsolatedRPC(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("native Pi qualification requires NEXUS_PI_NATIVE=1")
	}
	executable, e := exec.LookPath("pi")
	if e != nil {
		t.Fatal(e)
	}
	bytes, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(bytes)
	var calls, released atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens           int `json:"max_tokens"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
			Model               string
			Messages            []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []any
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "nexus-fixture" || len(request.Tools) != 0 {
			t.Error("provider request violated identity or tool policy")
			http.Error(w, "invalid", 400)
			return
		}
		if (request.MaxTokens == 0 && request.MaxCompletionTokens == 0) || request.MaxTokens > 1024 || request.MaxCompletionTokens > 1024 {
			t.Error("generation ceiling missing or exceeded", request.MaxTokens, request.MaxCompletionTokens)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"nexus-fixture","choices":[{"index":0,"delta":{"role":"assistant","content":"native Pi result"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"nexus-fixture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	cfg := Config{ModelRevision: "fixture-v1", Prices: &Prices{}, Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Provider: "nexus-test", Model: "nexus-fixture", BaseURL: provider.URL + "/v1", APIKey: "fixture-only", ContextTokens: 16384, MaxOutputTokens: 1024, Timeout: 15 * time.Second, Admit: func(context.Context) (func(), error) { return func() { released.Add(1) }, nil }}
	result, e := Run(context.Background(), cfg, "Return a short answer.")
	if e != nil || result.Text != "native Pi result" || result.Provider != "nexus-test" || result.Model != "nexus-fixture" {
		t.Fatal(result, e)
	}
	verifyNativeLearningBoundary(t, result)
	expectedIdentity, identityErr := cfg.Identity()
	if identityErr != nil || result.Identity != expectedIdentity || result.Usage == nil || *result.Usage.Input != 20 || *result.Usage.Output != 4 || *result.Usage.TotalTokens != 24 {
		t.Fatal("lost native provenance/usage", result, identityErr)
	}
	if calls.Load() != 1 || released.Load() != 1 {
		t.Fatal("execution or reservation release count", calls.Load(), released.Load())
	}
}

func TestNativePiCancellation(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("native Pi qualification requires NEXUS_PI_NATIVE=1")
	}
	executable, e := exec.LookPath("pi")
	if e != nil {
		t.Fatal(e)
	}
	body, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls, released atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer provider.Close()
	cfg := Config{ModelRevision: "fixture-v1", Prices: &Prices{}, Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Provider: "nexus-test", Model: "nexus-fixture", BaseURL: provider.URL + "/v1", APIKey: "fixture-only", ContextTokens: 16384, MaxOutputTokens: 1024, Timeout: 15 * time.Second, Admit: func(context.Context) (func(), error) { return func() { released.Add(1) }, nil }}
	result, err := Run(ctx, cfg, "Return an answer.")
	if err == nil || result.Text != "" || calls.Load() != 1 || released.Load() != 1 {
		t.Fatal("cancellation returned success, retried, or leaked reservation", result, err, calls.Load(), released.Load())
	}
}

// The fixture host supplies canonical ownership and a deterministic fixture
// evaluator; neither adapter success nor imported self-reports create a vote.
func verifyNativeLearningBoundary(t *testing.T, result Result) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	sum := sha256.Sum256([]byte(result.Text))
	task := harness.TaskClass{Domain: "fixture", Profile: "exact-answer-v1", Difficulty: "easy"}
	execution := harness.Execution{Version: 1, ID: "native-fixture-attempt", Actual: result.Identity, Task: task, Status: "completed", OutputSHA256: hex.EncodeToString(sum[:]), CompletedAt: now}
	if err := ledger.AppendExecution(ctx, execution, now); err != nil {
		t.Fatal(err)
	}
	candidate := harness.Candidate{Identity: result.Identity, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 16384}
	request := harness.Request{Version: 1, Task: task, Mode: "local_only", ContextTokens: 100}
	selectCurrent := func() harness.Selection {
		snapshot, e := ledger.Snapshot(ctx, now)
		if e != nil {
			t.Fatal(e)
		}
		selected, e := harness.Select(request, harness.DefaultPolicy(), []harness.Candidate{candidate}, snapshot, now, .9)
		if e != nil {
			t.Fatal(e)
		}
		return selected
	}
	before := selectCurrent()
	if before.Primary.ConfirmedSamples != 0 || before.Primary.PendingOutputs != 1 {
		t.Fatal("execution success created a quality vote")
	}
	digest, err := execution.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "native Pi result" {
		t.Fatal("fixture exact-answer evaluator failed")
	}
	review := harness.Review{Version: 1, ID: "fixture-review", ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "exact-answer-v1", Reviewer: "native-fixture-host", Confidence: 1, Quality: 1, CreatedAt: now}
	if err := ledger.AppendReview(ctx, review, now); err != nil {
		t.Fatal(err)
	}
	after := selectCurrent()
	if after.Primary.ConfirmedSamples != 1 || after.Primary.PendingOutputs != 0 || after.Primary.Correctness <= before.Primary.Correctness {
		t.Fatal("bound evaluation did not affect selection")
	}
	candidate.Identity.ModelRevision = "new-unmeasured-weights"
	changed := selectCurrent()
	if changed.Primary.ConfirmedSamples != 0 || changed.Primary.Correctness != .5 {
		t.Fatal("changed model borrowed old quality")
	}
}
