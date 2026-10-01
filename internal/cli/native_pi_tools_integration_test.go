package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness/goose"
	"github.com/ArronJablonowski/NexusRouter/harness/openhands"
	"github.com/ArronJablonowski/NexusRouter/harness/pi"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"go.yaml.in/yaml/v3"
)

// Opt-in machine qualification uses the production resource profiler and installed
// harness, with a private CLI home/owner directory and no real model inference.
func TestCLINativePiHostTools(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi and measurable host capacity")
	}
	testCLINativeHostTools(t, "pi", pi.AgentAdapterVersion)
}
func TestCLINativeOpenHandsHostTools(t *testing.T) {
	if os.Getenv("NEXUS_OPENHANDS_PYTHON") == "" {
		t.Skip("requires installed OpenHands and measurable capacity")
	}
	testCLINativeHostTools(t, "openhands", openhands.AgentAdapterVersion)
}
func testCLINativeHostTools(t *testing.T, kind, adapter string) {
	executable, e := exec.LookPath("pi")
	if kind == "goose" {
		executable = "/Users/aj_lobster/Documents/Codex/2026-09-19/do-x20/outputs/harness-runtime/goose-1.52.0/goose"
		e = nil
	}
	var runtimeDigest string
	if kind == "openhands" {
		executable = os.Getenv("NEXUS_OPENHANDS_PYTHON")
		var manifest []byte
		manifest, e = os.ReadFile(os.Getenv("NEXUS_OPENHANDS_MANIFEST"))
		digest := sha256.Sum256(manifest)
		runtimeDigest = hex.EncodeToString(digest[:])
	}
	if e != nil {
		t.Fatal(e)
	}
	artifact, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	pin := sha256.Sum256(artifact)
	binary := filepath.Join(t.TempDir(), "nexus")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/nexus")
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatal(e, string(output))
	}
	for _, mode := range []string{"text", "json", "deny_create"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "read_file") || strings.Contains(string(body), "cli-private-secret") {
					t.Error("lost host tool catalogue or leaked secret")
				}
				if n > 2 {
					t.Error("repeated inference")
				}
				w.Header().Set("Content-Type", "application/x-ndjson")
				if n == 1 {
					name, args := "read_file", map[string]string{"path": "input.txt"}
					if mode == "deny_create" {
						name, args = "create_file", map[string]string{"path": "unapproved.txt", "content": "not authorized"}
					}
					data, _ := json.Marshal(map[string]any{"model": "fixture", "message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"function": map[string]any{"name": name, "arguments": args}}}}, "done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 2})
					fmt.Fprintf(w, "%s\n", data)
					return
				}
				if !strings.Contains(string(body), "CLI rooted evidence") {
					t.Error("missing file result")
				}
				fmt.Fprintln(w, `{"model":"fixture","message":{"role":"assistant","content":"CLI native answer"},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":4}`)
			}))
			defer provider.Close()
			root := t.TempDir()
			if e := os.WriteFile(filepath.Join(root, "input.txt"), []byte("CLI rooted evidence cli-private-secret"), 0600); e != nil {
				t.Fatal(e)
			}
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Tools.Enabled = true
			cfg.Tools.ReadRoot = root
			cfg.Tools.MaxTurns = 3
			cfg.Runtime.MaxTurns = 3
			cfg.Hardware.Concurrent = "1"
			cfg.Workers.Max = 1
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
			cfg.Security.RedactEnv = []string{"NEXUS_CLI_TEST_SECRET"}
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL, RequestTimeout: "15s"}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
			cfg.NativeHarnesses = []config.NativeHarness{{ID: "native-tools", Kind: kind, RuntimeSHA256: runtimeDigest, NativeTools: true, ModelID: "chat", Executable: executable, ExecutableSHA256: hex.EncodeToString(pin[:]), ModelRevision: "fixture-v1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &config.NativeHarnessPrices{}}}
			if mode == "deny_create" {
				cfg.Tools.CreateEnabled = true
				cfg.Tools.CreateRoot = root
			}
			data, e := yaml.Marshal(cfg)
			if e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(t.TempDir(), "config.yaml")
			if e = os.WriteFile(file, data, 0600); e != nil {
				t.Fatal(e)
			}
			args := []string{"run", "--config", file, "--model", "chat", "--harness", "native-tools", "--domain", "writing", "--profile", "cli-fixture"}
			if mode != "text" {
				args = append(args, "--json")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "NEXUS_CLI_TEST_SECRET=cli-private-secret"}
			command.Stdin = strings.NewReader("Read the configured file then answer.")
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			runErr := command.Run()
			if strings.Contains(stdout.String()+stderr.String(), "cli-private-secret") {
				t.Fatal("CLI exposed secret")
			}
			var taskID string
			if mode == "text" {
				if runErr != nil || strings.TrimSpace(stdout.String()) != "CLI native answer" {
					t.Fatal(runErr, stdout.String(), stderr.String())
				}
				for _, line := range strings.Split(stderr.String(), "\n") {
					if strings.HasPrefix(line, "Task: ") {
						taskID = strings.TrimPrefix(line, "Task: ")
					}
				}
			} else {
				lines := readJSONLines(t, stdout.String())
				for _, line := range lines {
					if string(line["type"]) == `"event"` {
						var event runtime.Event
						if json.Unmarshal(line["event"], &event) != nil {
							t.Fatal("invalid event")
						}
						taskID = event.TaskID
					}
				}
				if mode == "json" && (runErr != nil || !strings.Contains(stdout.String(), "CLI native answer") || !strings.Contains(stdout.String(), `"input_tokens":30`) || !strings.Contains(stdout.String(), `"output_tokens":6`)) {
					t.Fatal(runErr, stdout.String(), stderr.String())
				}
				if mode == "deny_create" && runErr == nil {
					t.Fatal("CLI implicitly approved a write")
				}
			}
			if mode == "deny_create" && taskID == "" {
				if !strings.Contains(stdout.String(), `"error":"admission_denied"`) {
					t.Fatal("wrong refusal", stdout.String(), stderr.String())
				}
				if calls.Load() != 0 {
					t.Fatal("unapproved CLI write dispatched", calls.Load())
				}
				if _, e = os.Stat(filepath.Join(root, "unapproved.txt")); !os.IsNotExist(e) {
					t.Fatal("unapproved file exists", e)
				}
				if _, e = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(e) {
					t.Fatal("rejected CLI task opened storage", e)
				}
				return
			}
			db, e := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			events, e := db.Read(context.Background(), taskID, 0, 100)
			if e != nil || len(events) == 0 || events[0].Data.Harness == nil || events[0].Data.Harness.Protocol != runtime.HarnessAgentProtocol {
				t.Fatal("CLI lost native journal", taskID, e, stderr.String())
			}
			last := events[len(events)-1]
			if mode == "deny_create" {
				if _, e = os.Stat(filepath.Join(root, "unapproved.txt")); !os.IsNotExist(e) || last.Kind != runtime.TaskFailed || last.Data.HarnessOutcome != nil || calls.Load() != 1 {
					t.Fatal("unapproved write or retry", e, last, calls.Load())
				}
				return
			}
			usage, e := runtime.ValidateHarnessAgentJournal(events, taskID)
			if e != nil || last.Kind != runtime.TaskCompleted || last.Data.HarnessOutcome == nil || last.Data.HarnessOutcome.Actual.AdapterVersion != adapter || calls.Load() != 2 || usage == nil || usage.InputTokens != 30 || usage.OutputTokens != 6 {
				t.Fatal("invalid CLI completion", last, usage, e, calls.Load())
			}
		})
	}
}

func TestCLINativeGooseHostTools(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("requires installed Goose")
	}
	testCLINativeHostTools(t, "goose", goose.AgentAdapterVersion)
}
