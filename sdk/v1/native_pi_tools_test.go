package v1_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness/goose"
	"github.com/ArronJablonowski/NexusRouter/harness/openhands"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKNativePiHostTools(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi")
	}
	nativeSDKHostTools(t, "pi", pi.AgentAdapterVersion)
}

func TestSDKNativeOpenHandsHostTools(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("requires installed OpenHands")
	}
	nativeSDKHostTools(t, "openhands", openhands.AgentAdapterVersion)
}

func nativeSDKHostTools(t *testing.T, kind, adapterVersion string) {
	for _, mode := range []string{"read", "create", "deny", "contract", "escape", "auto", "queue", "configured"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("outside-root-sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("host file content native-fixture-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			var calls, reviews atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"data":[{"id":"fixture","object":"model"}]}`)
					return
				}
				n := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "read_file") {
					t.Error("host tools missing")
				}
				if strings.Contains(string(body), "outside-root-sentinel") {
					t.Error("file root escaped")
				}
				if n == 2 && strings.Contains(string(body), "native-fixture-secret") {
					t.Error("secret tool output reached provider")
				}
				if n == 2 && mode == "read" && !strings.Contains(string(body), "host file content") {
					t.Error("missing scoped file result")
				}
				name, args := "read_file", `{"path":"input.txt"}`
				if mode == "create" || mode == "deny" {
					name, args = "create_file", `{"path":"created.txt","content":"approved content"}`
				}
				if mode == "escape" {
					args = `{"path":"../outside.txt"}`
				}
				delta := map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "host-tool-1", "type": "function", "function": map[string]string{"name": name, "arguments": args}}}}
				finish := "tool_calls"
				if n == 2 {
					delta = map[string]any{"role": "assistant", "content": "native host answer"}
					finish = "stop"
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, piece := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]string{}, "finish_reason": finish}} {
					response := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{piece}}
					if piece["finish_reason"] != nil {
						response["usage"] = map[string]int{"prompt_tokens": 20, "completion_tokens": 4, "total_tokens": 24}
					}
					b, _ := json.Marshal(response)
					fmt.Fprintf(w, "data: %s\n\n", b)
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			ledger, e := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
			if e != nil {
				t.Fatal(e)
			}
			defer ledger.Close()
			var settings config.Settings
			var registrations []sdk.NativeHarness
			client, _ := nativeSDKProviderClient(t, server.URL, 64<<20, true, "openai_compatible", func(c *config.Settings, o *sdk.ConfigOptions) {
				o.HarnessEvidence = ledger
				c.Tools.ReadRoot = root
				c.Runtime.MaxTurns = 3
				c.Tools.MaxTurns = 3
				o.NativeHarnesses = []sdk.NativeHarness{nativeRegistration(t, kind)}
				o.NativeHarnesses[0].NativeTools = true
				registrations = o.NativeHarnesses
				if mode == "create" || mode == "deny" {
					c.Tools.CreateEnabled = true
					c.Tools.CreateRoot = root
					o.ApprovalReviewer = func(_ context.Context, p sdk.ApprovalPrompt) (string, bool, error) {
						reviews.Add(1)
						if !strings.Contains(string(p.Arguments), "created.txt") {
							t.Error("wrong approval arguments")
						}
						return "fixture-operator", mode == "create", nil
					}
				}
				if mode == "configured" {
					h := o.NativeHarnesses[0]
					c.NativeHarnesses = []config.NativeHarness{{ID: h.ID, Kind: h.Kind, ModelID: h.ModelID, NativeTools: true, Executable: h.Executable, ExecutableSHA256: h.ExecutableSHA256, RuntimeSHA256: h.RuntimeSHA256, ModelRevision: h.ModelRevision, MaxOutputTokens: h.MaxOutputTokens, OverheadRAMBytes: h.OverheadRAMBytes, Prices: &config.NativeHarnessPrices{}}}
					o.NativeHarnesses = nil
				}
				settings = *c
			})
			prompt := "Read the host file then answer."
			if mode == "contract" {
				prompt = "Return only valid JSON."
			}
			req := sdk.Request{Version: 1, HarnessID: kind + "-fixture", ModelID: "chat", Messages: []providers.Message{{Role: "user", Content: prompt}}, Domain: "writing", Profile: "tools-fixture"}
			if mode == "auto" {
				req.HarnessID = "auto"
				req.ModelID = "auto"
				req.ContextTokens = 16384
			}
			var result sdk.Result
			var err error
			if mode == "queue" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				queued, e := client.Submit(ctx, "native-tools-queue-fixture", req)
				if e != nil {
					t.Fatal(e)
				}
				service, e := app.NewServiceWithProfiler(settings, func(string) string { return "native-fixture-secret" }, sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }))
				if e != nil {
					t.Fatal(e)
				}
				if e = service.ConfigureNativeHarnesses(registrations, ledger); e != nil {
					t.Fatal(e)
				}
				dispatcher, e := app.StartDispatcher(ctx, service)
				if e != nil {
					t.Fatal(e)
				}
				defer dispatcher.Close()
				for {
					status, e := client.SubmissionStatus(ctx, queued.ID)
					if e != nil {
						t.Fatal(e)
					}
					if status.State != "queued" && status.State != "running" {
						if status.State != "succeeded" || status.Result == nil {
							t.Fatal(status)
						}
						result = sdk.Result{TaskID: status.Result.TaskID, Text: status.Result.Text, Turns: status.Result.Turns, Usage: status.Result.Usage}
						outcome, e := client.ReconcileHarnessOutcome(ctx, ledger, result.TaskID)
						if e != nil {
							t.Fatal(e)
						}
						result.HarnessOutcome = &outcome
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(20 * time.Millisecond):
					}
				}
				again, e := client.Submit(ctx, "native-tools-queue-fixture", req)
				if e != nil || again.ID != queued.ID || calls.Load() != 2 {
					t.Fatal("replayed native tools", again, e, calls.Load())
				}
			} else {
				result, err = client.Run(context.Background(), req)
			}
			if mode == "auto" && (result.HarnessSelection == nil || result.HarnessSelection.Primary.Identity.AdapterVersion != adapterVersion) {
				t.Fatal("automatic routing lost tools identity", result, err)
			}

			if mode == "contract" || mode == "deny" {
				if err == nil || result.HarnessOutcome != nil || result.Text != "" {
					t.Fatal("invalid success", result, err)
				}
			} else if err != nil || result.Text != "native host answer" || result.HarnessOutcome == nil || result.HarnessOutcome.Actual.AdapterVersion != adapterVersion || result.Turns != 2 || result.Usage == nil || result.Usage.InputTokens != 40 || result.Usage.OutputTokens != 8 {
				t.Fatal("host tool run", result, err, calls.Load())
			}
			data, readErr := os.ReadFile(filepath.Join(root, "created.txt"))
			if mode == "create" {
				if readErr != nil || string(data) != "approved content" || reviews.Load() != 1 {
					t.Fatal("approved write missing", readErr, reviews.Load())
				}
			}
			if mode == "deny" {
				if !os.IsNotExist(readErr) || reviews.Load() != 1 {
					t.Fatal("denied write executed", readErr, reviews.Load())
				}
			}
			if mode == "contract" && calls.Load() != 2 {
				t.Fatal("contract failed before final answer", calls.Load())
			}
			page, e := client.ReadEvents(context.Background(), result.TaskID, 0, 100)
			if e != nil || len(page.Events) == 0 || page.Events[0].Data.Harness.Protocol != runtime.HarnessAgentProtocol || page.Events[0].Data.Privacy != "local_only" {
				t.Fatal("missing native journal", page, e)
			}
		})
	}
}

func TestSDKNativeGooseHostTools(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("requires installed Goose")
	}
	nativeSDKHostTools(t, "goose", goose.AgentAdapterVersion)
}
