package v1_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

func nativeSDKClient(t *testing.T, endpoint string, overhead uint64, tools bool) (*sdk.Client, *atomic.Int32) {
	return nativeSDKProviderClient(t, endpoint, overhead, tools, "openai_compatible")
}
func nativeSDKProviderClient(t *testing.T, endpoint string, overhead uint64, tools bool, providerKind string, configure ...func(*config.Settings, *sdk.ConfigOptions)) (*sdk.Client, *atomic.Int32) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = tools
	cfg.Hardware.Concurrent = "1"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Providers = []config.Provider{{ID: "native-local", Kind: providerKind, Endpoint: endpoint, APIKeyEnv: "NATIVE_TEST_SECRET", RequestTimeout: "15s"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "native-local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat", "writing"}, EstimatedCost: &zero}}
	extra := sdk.ConfigOptions{}
	for _, change := range configure {
		change(&cfg, &extra)
	}
	body, e := yaml.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if e = os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
	profiled := &atomic.Int32{}
	var registrations []sdk.NativeHarness
	if len(cfg.NativeHarnesses) == 0 || extra.NativeHarnesses != nil {
		registrations = nativeFixtureRegistrations(t, extra.NativeHarnesses, overhead)
	}
	client, e := sdk.New(sdk.ConfigOptions{Evaluator: extra.Evaluator, HarnessEvidence: extra.HarnessEvidence, ProjectFile: path, LookupSecret: func(name string) string {
		if name == "NATIVE_TEST_SECRET" {
			return "native-fixture-secret"
		}
		return ""
	}, ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
		profiled.Add(1)
		return sdkGoodMeasurement(), nil
	}), NativeHarnesses: registrations})
	if e != nil {
		t.Fatal(e)
	}
	return client, profiled
}
func nativeFixtureRegistrations(t *testing.T, overrides []sdk.NativeHarness, overhead uint64) []sdk.NativeHarness {
	t.Helper()
	if len(overrides) > 0 {
		return overrides
	}
	executable, e := exec.LookPath("pi")
	if e != nil {
		t.Fatal(e)
	}
	body, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(body)
	pin := hex.EncodeToString(hash[:])
	return []sdk.NativeHarness{{ID: "pi-fixture", ModelID: "chat", Kind: "pi", Executable: executable, ExecutableSHA256: pin, ModelRevision: "fixture-v1", MaxOutputTokens: 1024, Prices: &pi.Prices{}, OverheadRAMBytes: overhead}}
}
func TestSDKNativePiUsesAdmissionContextAndDurableEvents(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	var calls atomic.Int32
	messages := []providers.Message{{Role: "system", Content: "Retain this exact system instruction."}, {Role: "user", Content: "Write a brief response."}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model               string
			Messages            []providers.Message
			MaxTokens           int `json:"max_tokens"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "fixture" || r.URL.Path != "/custom-api/chat/completions" || r.Header.Get("Authorization") != "Bearer native-fixture-secret" || len(request.Messages) != len(messages) {
			t.Error("SDK native request lost admitted context or endpoint")
			http.Error(w, "invalid", 400)
			return
		}
		for i, m := range messages {
			if request.Messages[i].Role != m.Role || request.Messages[i].Content != m.Content {
				t.Error("message roles/content changed")
			}
		}
		if request.MaxTokens+request.MaxCompletionTokens != 1024 {
			t.Error("output ceiling changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"sdk-fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"role":"assistant","content":"SDK native result"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"sdk-fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client, profiled := nativeSDKClient(t, server.URL+"/custom-api", 64<<20, false)
	req := sdk.Request{Version: 1, HarnessID: "pi-fixture", ModelID: "chat", Messages: messages, Domain: "writing", Profile: "fixture-v1"}
	var delivered strings.Builder
	result, err := client.RunTextStream(context.Background(), req, func(text string) error { delivered.WriteString(text); return nil })
	if err != nil || result.Text != "SDK native result" || delivered.String() != result.Text || result.HarnessOutcome == nil || result.HarnessOutcome.Validate() != nil || profiled.Load() == 0 || calls.Load() != 1 {
		t.Fatal("SDK native path failed", result, err, profiled.Load(), calls.Load(), delivered.String())
	}
	page, err := client.ReadEvents(context.Background(), result.TaskID, 0, 10)
	if err != nil || page.State != "completed" || len(page.Events) != 2 || page.Events[1].Data.HarnessOutcome == nil {
		t.Fatal("missing canonical SDK native events", page, err)
	}

	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "learning"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	outcome, err := client.ReconcileHarnessOutcome(context.Background(), ledger, result.TaskID)
	if err != nil || outcome != *result.HarnessOutcome {
		t.Fatal("SDK reconciliation lost native provenance", outcome, err)
	}
	digest, _ := outcome.Digest()
	review := harness.Review{Version: 1, ID: "native-fixture-review", ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "exact-fixture-text-v1", Reviewer: "sdk-native-test", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
	// This test's explicit equality check above supplies the fixture verdict;
	// ordinary completion never synthesizes a review.
	if err := client.ReviewHarnessOutcome(context.Background(), ledger, result.TaskID, review); err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 4 {
		t.Fatal("verified provider measurement missing")
	}
	if queued, err := client.Submit(context.Background(), "native-queue-registered", req); err != nil || queued.State != "queued" {
		t.Fatal("registered native request failed durable admission", queued, err)
	}
	req.HarnessID = "unknown"
	if _, err := client.Run(context.Background(), req); !errors.Is(err, sdk.ErrHarnessUnsupported) {
		t.Fatal(err)
	}
	req.HarnessID = "pi-fixture"
	req.ModelID = "auto"
	if _, err := client.Run(context.Background(), req); !errors.Is(err, sdk.ErrHarnessUnsupported) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("unsupported route silently dispatched")
	}
}
func TestSDKNativePiRejectsUnreservedCapacityAndTools(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, tools := range []bool{false, true} {
		client, _ := nativeSDKClient(t, server.URL, 1<<62, tools)
		_, err := client.Run(context.Background(), sdk.Request{Version: 1, HarnessID: "pi-fixture", ModelID: "chat", Prompt: "Write briefly."})
		if err == nil {
			t.Fatal("unavailable native capacity/tool policy admitted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("inference before admission")
	}
}

func TestSDKNativePiCancellationAndResponseContract(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if cancelRun {
					cancel()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"role":"assistant","content":"not JSON"},"finish_reason":null}]}`+"\n\n")
				fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			client, _ := nativeSDKClient(t, server.URL, 64<<20, false)
			result, err := client.Run(ctx, sdk.Request{Version: 1, HarnessID: "pi-fixture", ModelID: "chat", Prompt: "Return only valid JSON.", Domain: "writing"})
			if err == nil || result.Text != "" || result.HarnessOutcome != nil || calls.Load() != 1 {
				t.Fatal("invalid/canceled output accepted or retried", result, err, calls.Load())
			}
			page, e := client.ReadEvents(context.Background(), result.TaskID, 0, 10)
			if e != nil || (page.State != "failed" && page.State != "canceled") || len(page.Events) != 2 || page.Events[1].Data.HarnessOutcome != nil {
				t.Fatal("missing failed native lineage", page, e)
			}
		})
	}
}

func TestSDKPiMeasuredContextAndEvidence(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("native Pi required")
	}
	nativeSDKContextAndEvidence(t, "pi")
}
