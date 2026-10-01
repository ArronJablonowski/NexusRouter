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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/hermes"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func nativeRegistration(t *testing.T, kind string) sdk.NativeHarness {
	t.Helper()
	var executable, source, runtimeDigest string
	var err error
	if kind == "hermes" {
		source = "/Users/aj_lobster/.hermes/hermes-agent"
		revision, e := exec.Command("git", "-C", source, "rev-parse", "HEAD").Output()
		if e != nil || strings.TrimSpace(string(revision)) != hermes.SupportedRevision {
			t.Fatal("Hermes source revision changed")
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))[:16]
		facts, e := os.ReadFile(filepath.Join("/Users/aj_lobster/.hermes/installs", key, "facts.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r struct {
			Packages struct{ Venv struct{ Environment string } }
		}
		if json.Unmarshal(facts, &r) != nil || r.Packages.Venv.Environment == "" {
			t.Fatal("missing Hermes runtime")
		}
		executable = filepath.Join(r.Packages.Venv.Environment, "bin", "python")
		runtimeDigest = fmt.Sprintf("%x", sha256.Sum256(facts))
	} else if kind == "openhands" {
		executable = os.Getenv("NEXUS_OPENHANDS_PYTHON")
		manifest, e := os.ReadFile(os.Getenv("NEXUS_OPENHANDS_MANIFEST"))
		if e != nil {
			t.Fatal(e)
		}
		runtimeDigest = fmt.Sprintf("%x", sha256.Sum256(manifest))
	} else if kind == "goose" {
		executable = "/Users/aj_lobster/Documents/Codex/2026-09-19/do-x20/outputs/harness-runtime/goose-1.52.0/goose"
	} else {
		executable, err = exec.LookPath(kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return sdk.NativeHarness{HermesSourceDir: source, RuntimeSHA256: runtimeDigest, ID: kind + "-fixture", ModelID: "chat", Kind: kind, Executable: executable, ExecutableSHA256: hex.EncodeToString(sum[:]), ModelRevision: "fixture-v1", MaxOutputTokens: 1024, Prices: &pi.Prices{}, OverheadRAMBytes: 64 << 20}
}

func TestSDKOpenClawPreservesContextAndEvidence(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_NATIVE") != "1" {
		t.Skip("requires installed OpenClaw")
	}
	nativeSDKContextAndEvidence(t, "openclaw")
}
func nativeSDKContextAndEvidence(t *testing.T, kind string) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model    string
			Messages []providers.Message
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "fixture" || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[0].Content != "Exact host policy." || request.Messages[1].Content != "Answer briefly." || r.Header.Get("Authorization") != "Bearer native-fixture-secret" {
			t.Errorf("%s lost host context or credentials", kind)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"role":"assistant","content":"correct answer"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	registration := nativeRegistration(t, kind)
	client, profiled := nativeSDKProviderClient(t, server.URL, 64<<20, false, "openai_compatible", func(c *config.Settings, o *sdk.ConfigOptions) {
		c.Providers[0].RequestTimeout = "30s"
		o.NativeHarnesses = []sdk.NativeHarness{registration}
	})
	var delivered strings.Builder
	result, err := client.RunTextStream(context.Background(), sdk.Request{Version: 1, ModelID: "chat", HarnessID: registration.ID, Domain: "writing", Profile: "fixture-v1", Messages: []providers.Message{{Role: "system", Content: "Exact host policy."}, {Role: "user", Content: "Answer briefly."}}}, func(text string) error { delivered.WriteString(text); return nil })
	if err != nil || result.Text != "correct answer" || delivered.String() != result.Text || result.HarnessOutcome == nil || result.HarnessOutcome.Actual.Harness != kind || result.Usage != nil || profiled.Load() == 0 || calls.Load() != 1 {
		t.Fatal("native SDK failed", result, err)
	}
	page, err := client.ReadEvents(context.Background(), result.TaskID, 0, 10)
	if err != nil || page.State != "completed" || len(page.Events) != 2 || page.Events[1].Data.HarnessOutcome == nil {
		t.Fatal("missing native journal", err)
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	outcome, err := client.ReconcileHarnessOutcome(context.Background(), ledger, result.TaskID)
	if err != nil || outcome != *result.HarnessOutcome {
		t.Fatal("lost execution identity", err)
	}
}

func TestSDKAutoLearnsBetweenHarnessesOnSameModel(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_NATIVE") != "1" || os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed OpenClaw and Pi")
	}
	nativeSDKLearnsPair(t, "openclaw")
}
func nativeSDKLearnsPair(t *testing.T, other string) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"fixture answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	registrations := []sdk.NativeHarness{nativeRegistration(t, "pi"), nativeRegistration(t, other)}
	setup := func(c *config.Settings, o *sdk.ConfigOptions) {
		c.Providers[0].RequestTimeout = "30s"
		o.NativeHarnesses = registrations
		o.HarnessEvidence = ledger
	}
	client, _ := nativeSDKProviderClient(t, server.URL, 64<<20, false, "ollama", setup)
	reviews := map[string]harness.Review{}
	taskIDs := map[string]string{}
	for _, entry := range registrations {
		result, e := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", HarnessID: entry.ID, Prompt: "Fixture answer.", Domain: "writing", Profile: "comparison-v1", ContextTokens: 16384})
		if e != nil || result.Text != "fixture answer" || result.HarnessOutcome == nil || result.HarnessOutcome.Actual.Harness != entry.Kind {
			t.Fatal("wrong native pair", result, e)
		}
		digest, _ := result.HarnessOutcome.Digest()
		verdict, quality := "failed", 0.0
		if entry.Kind == other {
			verdict, quality = "passed", 1
		}
		// Controlled evidence exercises pair selection; these labels are not a
		// comparative quality claim about real harness outputs.
		review := harness.Review{Version: 1, ID: entry.Kind + "-review", ExecutionDigest: digest, Verdict: verdict, Method: "deterministic", MethodVersion: "controlled-fixture-v1", Reviewer: "sdk-test", Confidence: 1, Quality: quality, CreatedAt: time.Now().UTC()}
		if e := client.ReviewHarnessOutcome(context.Background(), ledger, result.TaskID, review); e != nil {
			t.Fatal(e)
		}
		reviews[entry.Kind] = review
		taskIDs[entry.Kind] = result.TaskID
	}
	req := sdk.Request{Version: 1, ModelID: "auto", HarnessID: "auto", Prompt: "Fixture answer.", Domain: "writing", Profile: "comparison-v1", ContextTokens: 16384}
	result, err := client.Run(context.Background(), req)
	if err != nil || result.HarnessOutcome == nil || result.HarnessOutcome.Actual.Harness != other || result.HarnessSelection == nil || result.HarnessSelection.Primary.ConfirmedSamples != 1 || result.HarnessSelection.Primary.Identity != result.HarnessOutcome.Actual {
		t.Fatal("did not select learned pair", result, err)
	}
	// Reverse current review heads; the next selection must learn the new outcome
	// for each harness, without altering or rerunning the two original executions.
	for _, kind := range []string{"pi", other} {
		head := reviews[kind]
		next := head
		next.ID += "-revision"
		next.ExpectedHead = head.ID
		next.CreatedAt = time.Now().UTC()
		next.Verdict = "failed"
		next.Quality = 0
		if kind == "pi" {
			next.Verdict = "passed"
			next.Quality = 1
		}
		if e := client.ReviewHarnessOutcome(context.Background(), ledger, taskIDs[kind], next); e != nil {
			t.Fatal(e)
		}
	}
	result, err = client.Run(context.Background(), req)
	if err != nil || result.HarnessOutcome == nil || result.HarnessOutcome.Actual.Harness != "pi" || calls.Load() != 4 {
		t.Fatal("updated evidence did not switch harness without replay", result, err, calls.Load())
	}
}
