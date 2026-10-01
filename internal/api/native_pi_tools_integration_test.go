package api

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/ArronJablonowski/NexusRouter/harness/hermes"
	"github.com/ArronJablonowski/NexusRouter/harness/openhands"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestHTTPNativePiHostTools(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	testHTTPNativeHostTools(t, "pi", pi.AgentAdapterVersion)
}
func TestHTTPNativeOpenHandsHostTools(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("requires installed OpenHands")
	}
	testHTTPNativeHostTools(t, "openhands", openhands.AgentAdapterVersion)
}
func testHTTPNativeHostTools(t *testing.T, kind, adapter string) {
	registration := nativeHTTPRegistration(t, kind)
	for _, mode := range []string{"plain", "stream", "contract", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			var calls atomic.Int32
			entered := make(chan struct{}, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				data, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(data), "read_file") || strings.Contains(string(data), token) {
					t.Error("lost tool catalogue or exposed secret")
				}
				if n == 2 && !strings.Contains(string(data), "scoped evidence") {
					t.Error("lost host tool result")
				}
				if n > 2 {
					t.Error("unexpected inference retry")
				}
				w.Header().Set("Content-Type", "application/x-ndjson")
				if n == 1 {
					fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"evidence.txt"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":2}`)
					return
				}
				if mode == "cancel" {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					entered <- struct{}{}
					<-r.Context().Done()
					return
				}
				fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"verified API answer"},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":4}`)
			}))
			defer func() { cancel(); provider.Close() }()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("scoped evidence "+token), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Tools.Enabled = true
			cfg.Tools.ReadRoot = root
			cfg.Tools.MaxTurns = 3
			cfg.Runtime.MaxTurns = 3
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "api.db")
			cfg.Security.RedactEnv = append(cfg.Security.RedactEnv, "API_NATIVE_TEST_SECRET")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL, RequestTimeout: "15s"}}
			cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}}}
			cfg.NativeHarnesses = []config.NativeHarness{registration}
			svc, err := app.NewServiceWithProfiler(cfg, func(name string) string {
				if name == "API_NATIVE_TEST_SECRET" {
					return token
				}
				return ""
			}, apiFixtureProfiler{})
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan app.Result, 1)
			service := services()
			service.Run = func(c context.Context, r app.Request) (app.Result, error) {
				out, e := svc.Run(c, r)
				results <- out
				return out, e
			}
			service.RunTextStream = func(c context.Context, r app.Request, emit func(string) error) (app.Result, error) {
				out, e := svc.RunTextStream(c, r, emit)
				results <- out
				return out, e
			}
			handler, err := New(token, 1, service)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer func() { cancel(); server.Close() }()
			prompt := "Read the file and answer."
			if mode == "contract" {
				prompt = "Return only valid JSON."
			}
			stream := mode != "plain"
			body, _ := json.Marshal(map[string]any{"model": "chat", "harness_id": "native-tools", "messages": []map[string]string{{"role": "user", "content": prompt}}, "stream": stream, "stream_options": map[string]bool{"include_usage": true}})
			if !stream {
				body, _ = json.Marshal(map[string]any{"model": "chat", "harness_id": "native-tools", "messages": []map[string]string{{"role": "user", "content": prompt}}})
			}
			// The real authenticated handler must reject before launching a harness or inference.
			unauthorized, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(string(body)))
			denied, e := server.Client().Do(unauthorized)
			if e != nil {
				t.Fatal(e)
			}
			denied.Body.Close()
			if denied.StatusCode != 401 || calls.Load() != 0 {
				t.Fatal("unauthorized dispatch", denied.Status, calls.Load())
			}
			requestCtx, stop := context.WithCancel(ctx)
			defer stop()
			req, _ := http.NewRequestWithContext(requestCtx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			response, e := server.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "cancel" {
				select {
				case <-entered:
					stop()
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			output, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if mode != "cancel" && (readErr != nil || response.StatusCode != 200) {
				t.Fatal(response.Status, readErr, string(output))
			}
			var out app.Result
			select {
			case out = <-results:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			db, e := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			events, e := db.Read(context.Background(), out.TaskID, 0, 100)
			if e != nil || len(events) == 0 || events[0].Data.Harness == nil || events[0].Data.Harness.Protocol != runtime.HarnessAgentProtocol || calls.Load() != 2 {
				t.Fatal("missing canonical tool lifecycle", out, e, calls.Load())
			}
			terminal := events[len(events)-1]
			if mode == "cancel" || mode == "contract" {
				want := runtime.TaskFailed
				if mode == "cancel" {
					want = runtime.TaskCanceled
				}
				if terminal.Kind != want || terminal.Data.HarnessOutcome != nil || out.HarnessOutcome != nil || out.Text != "" || strings.Contains(string(output), "[DONE]") || strings.Contains(string(output), "verified API answer") {
					t.Fatal("failed run exposed success", terminal, out, string(output))
				}
				return
			}
			if terminal.Kind != runtime.TaskCompleted || out.Turns != 2 || out.Usage == nil || out.Usage.InputTokens != 30 || out.Usage.OutputTokens != 6 || out.HarnessOutcome == nil || out.HarnessOutcome.Actual.AdapterVersion != adapter || !strings.Contains(string(output), "verified API answer") || strings.Contains(string(output), token) {
				t.Fatal("lost verified API result", out, string(output))
			}
			if !strings.Contains(string(output), `"prompt_tokens":30`) || !strings.Contains(string(output), `"completion_tokens":6`) {
				t.Fatal("wire response lost verified multi-turn usage", string(output))
			}
			if stream && !strings.Contains(string(output), "[DONE]") {
				t.Fatal("missing stream completion")
			}
		})
	}
}

func TestHTTPNativeGooseHostTools(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("requires installed Goose")
	}
	testHTTPNativeHostTools(t, "goose", goose.AgentAdapterVersion)
}

func TestHTTPNativeHermesHostTools(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" {
		t.Skip("requires installed Hermes")
	}
	testHTTPNativeHostTools(t, "hermes", hermes.AgentAdapterVersion)
}
