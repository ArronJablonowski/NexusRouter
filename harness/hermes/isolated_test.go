package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsolatedFiles(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-inherit")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path, env, err := isolatedFiles(dir, "http://127.0.0.1:12345/v1", "fixture-key", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private config missing")
	}
	for _, v := range env {
		if strings.Contains(v, "must-not-inherit") || strings.HasPrefix(v, "OPENAI_API_KEY=") {
			t.Fatal("credential inherited")
		}
	}
	if _, _, err = isolatedFiles(dir, "http://127.0.0.1:12345/v1", "fixture-key", "fixture"); err == nil {
		t.Fatal("reused directory")
	}
	for _, u := range []string{"https://127.0.0.1:12345/v1", "http://localhost:12345/v1", "http://127.0.0.1/v1", "http://127.0.0.1:12345/v1?x=1"} {
		d := t.TempDir()
		_ = os.Chmod(d, 0700)
		if _, _, err := isolatedFiles(d, u, "fixture-key", "fixture"); err == nil {
			t.Fatal("accepted non-gateway", u)
		}
	}
}

// Qualifies the actual installed CLI using only a disposable fixture endpoint.
// This qualifies response verification, not production process admission/cleanup.
func TestNativeIsolatedChat(t *testing.T) {
	if os.Getenv("NEXUS_HERMES_NATIVE") != "1" {
		t.Skip("native Hermes qualification is opt-in")
	}
	for _, good := range []bool{true, false} {
		t.Run(fmt.Sprint(good), func(t *testing.T) { nativeGatewayFixture(t, good, false) })
	}
}
func nativeGatewayFixture(t *testing.T, good, runner bool) {
	root := "/Users/aj_lobster/.hermes/hermes-agent"
	revision, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(revision)) != SupportedRevision {
		t.Fatal("installed revision changed")
	}
	launcher := filepath.Join(root, ".hermes/bin/hermes")
	before, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		after, err := os.ReadFile(launcher)
		if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Error("native invocation changed installed launcher")
		}
	}()
	var calls atomic.Int32
	var invalid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model     string
			Tools     []json.RawMessage
			Stream    bool
			MaxTokens int `json:"max_tokens"`
			Messages  []struct{ Role, Content string }
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "fixture" || len(body.Tools) != 0 || body.MaxTokens != 128 || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "host-owned question" {
			t.Logf("unexpected fixture request: path=%s model=%s tools=%d", r.URL.Path, body.Model, len(body.Tools))
			invalid.Store(true)
			http.Error(w, "invalid request", 400)
			return
		}
		calls.Add(1)

		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			wire := "data: {\"id\":\"fixture-1\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture-1\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			if !good {
				wire = strings.Replace(wire, `"stop"`, `"length"`, 1)
			}
			fmt.Fprint(w, wire)
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"fixture-1","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, childKey, verified, closeGateway, err := textgateway.Start(ctx, textgateway.Config{BaseURL: server.URL + "/v1", APIKey: "fixture-key", Model: "fixture", ContextTokens: 32768, MaxOutputTokens: 128, DefaultMissingOutputLimit: true, Timeout: 30 * time.Second, Transport: http.DefaultTransport, Messages: []providers.Message{{Role: "user", Content: "host-owned question"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGateway()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	_, env, err := isolatedFiles(dir, base, childKey, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))[:16]
	facts, err := os.ReadFile(filepath.Join("/Users/aj_lobster/.hermes/installs", key, "facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Packages struct{ Venv struct{ Environment string } }
	}
	if json.Unmarshal(facts, &record) != nil || record.Packages.Venv.Environment == "" {
		t.Fatal("missing runtime")
	}
	python := filepath.Join(record.Packages.Venv.Environment, "bin", "python")
	if runner {
		artifact, err := os.ReadFile(python)
		if err != nil {
			t.Fatal(err)
		}
		admitted, released := 0, 0
		c := Config{Executable: python, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(artifact)), SourceDir: root, RuntimeSHA256: fmt.Sprintf("%x", sha256.Sum256(facts)), Provider: "fixture-provider", Model: "fixture", ModelRevision: "fixture-r1", BaseURL: server.URL + "/v1", APIKey: "fixture-key", TransportPolicySHA256: strings.Repeat("a", 64), ContextTokens: 32768, MaxOutputTokens: 128, Timeout: 30 * time.Second, Prices: &Prices{}, Messages: []providers.Message{{Role: "user", Content: "host-owned question"}}, Transport: http.DefaultTransport, Admit: func(context.Context) (func(), error) { admitted++; return func() { released++ }, nil }}
		journal, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "native.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		task := Task{ID: "hermes-fixture", SessionID: "session-fixture", Prompt: "Return answer.", Class: harness.TaskClass{Domain: "fixture", Profile: "exact-v1", Difficulty: "easy"}, MaxOutputBytes: 4096}
		taskResult, err := RunTask(ctx, journal, c, task)
		result := taskResult.Result
		if good && err == nil {
			events, e := journal.Read(ctx, task.ID, 0, 10)
			if e != nil || len(events) != 2 || events[1].Data.HarnessOutcome == nil || *events[1].Data.HarnessOutcome != taskResult.Execution {
				t.Fatal("canonical outcome missing", e)
			}
		}
		if _, e := RunTask(ctx, journal, c, task); e == nil {
			t.Fatal("reused task identity")
		}

		if (err == nil) != good || calls.Load() != 1 || invalid.Load() || admitted != 1 || released != 1 {
			t.Fatal("runner contract", err, calls.Load(), admitted, released)
		}
		if good && (result.Text != "answer" || result.Identity.Harness != "hermes" || result.Identity.Provider != "fixture-provider") {
			t.Fatal("runner attribution", result)
		}
		if !good && result != (Result{}) {
			t.Fatal("failed run exposed result")
		}
		return
	}
	bootstrap := "import sys; sys.path.insert(0," + strconv.Quote(root) + "); import hermes_bootstrap; from hermes_cli.main import main; sys.exit(main())"
	cmd := exec.CommandContext(ctx, python, "-I", "-c", bootstrap, "chat", "--model", "fixture", "--provider", "nexus-gateway", "--reasoning", "none", "--toolsets", "all", "--max-turns", "1", "--run-budget", "20", "--ignore-rules", "--query-file", "-", "--oneshot", "--format", "stream-json")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader("Return answer.")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if !good {
		_, verifyErr := verified()
		_, projectionErr := ParseProjection(output, 1, "fixture")
		if err == nil || verifyErr == nil || projectionErr == nil || calls.Load() != 1 || invalid.Load() {
			t.Fatal("truncated response escaped or repeated inference", err, verifyErr, calls.Load())
		}
		return
	}
	if err != nil {
		t.Fatalf("native execution: %v\n%s\n%s", err, output, stderr.String())
	}
	p, err := ParseProjection(output, 0, "fixture")
	if err != nil {
		t.Fatalf("projection: %v\n%s\n%s", err, output, stderr.String())
	}
	actual, err := verified()
	if err != nil || actual.Text != p.Text {
		t.Fatal("unverified native output", err)
	}
	if p.Text != "answer" || calls.Load() != 1 || invalid.Load() {
		t.Fatalf("result=%q calls=%d invalid=%v", p.Text, calls.Load(), invalid.Load())
	}
}
