package openclaw

import (
	"bytes"
	"context"
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
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIsolatedFiles(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-inherit")
	t.Setenv("NODE_OPTIONS", "--require=must-not-load")
	dir := privateDir(t)
	path, env, err := isolatedFiles(dir, "http://127.0.0.1:34567/v1", "ephemeral-child-token", "fixture", "model", 32768, 128)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	for _, v := range env {
		if strings.Contains(v, "must-not") || strings.HasPrefix(v, "HOME=") {
			t.Fatal("inherited unsafe environment")
		}
	}
	if _, _, err := isolatedFiles(dir, "http://127.0.0.1:34567/v1", "token", "fixture", "model", 32768, 128); err == nil {
		t.Fatal("reused nonempty state")
	}
	for _, url := range []string{"https://127.0.0.1:123/v1", "http://localhost:123/v1", "http://example.invalid:123/v1", "http://127.0.0.1:123/v1?x=1", "http://user@127.0.0.1:123/v1", "http://127.0.0.1/v1"} {
		if _, _, err := isolatedFiles(privateDir(t), url, "token", "fixture", "model", 32768, 128); err == nil {
			t.Fatalf("accepted non-gateway URL %s", url)
		}
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(privateDir(t), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := isolatedFiles(link, "http://127.0.0.1:123/v1", "token", "fixture", "model", 32768, 128); err == nil {
		t.Fatal("accepted state symlink")
	}
}

// Real installed CLI, disposable directories and only a local fixture provider.
// This qualifies the configuration recipe; it is not SDK/gateway integration.
func TestNativeIsolatedExec(t *testing.T) {
	if os.Getenv("NEXUS_OPENCLAW_NATIVE") != "1" {
		t.Skip("native OpenClaw configuration qualification is opt-in")
	}
	for _, protocol := range []string{"openai_compatible", "ollama"} {
		t.Run(protocol, func(t *testing.T) { runNativeFixture(t, protocol) })
	}
}

func runNativeFixture(t *testing.T, protocol string) {
	var pkg struct{ Version string }
	b, err := os.ReadFile("/opt/homebrew/lib/node_modules/openclaw/package.json")
	if err != nil || json.Unmarshal(b, &pkg) != nil || pkg.Version != SupportedVersion {
		t.Fatal("installed OpenClaw version changed")
	}
	var calls atomic.Int32
	var violation atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model               string
			Stream              bool
			Tools               []json.RawMessage
			Options             map[string]int
			MaxTokens           int `json:"max_tokens"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		}
		expectedPath := "/v1/chat/completions"
		if protocol == "ollama" {
			expectedPath = "/api/chat"
		}
		if calls.Add(1) != 1 || r.Method != "POST" || r.URL.Path != expectedPath || r.Header.Get("Authorization") != "Bearer ephemeral-fixture-only" || json.NewDecoder(io.LimitReader(r.Body, MaxEnvelopeBytes)).Decode(&body) != nil || body.Model != "test-model" || !body.Stream || len(body.Tools) != 0 || (protocol != "ollama" && body.MaxTokens+body.MaxCompletionTokens != 128) || (protocol == "ollama" && (body.Options["num_ctx"] != 32768 || body.Options["num_predict"] != 128)) {
			violation.Store(true)
			http.Error(w, "fixture contract violation", 400)
			return
		}

		if protocol == "ollama" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":"fixture answer"},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"fixture-1","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"fixture answer"},"finish_reason":null}]}`,
			`{"id":"fixture-1","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()
	dir := privateDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var policyCalls atomic.Int32
	upstreamURL := server.URL + "/v1"
	if protocol == "ollama" {
		upstreamURL = server.URL
	}
	base, key, verified, closeGateway, err := startGateway(ctx, gatewayConfig{BaseURL: upstreamURL, APIKey: "ephemeral-fixture-only", Model: "test-model", UpstreamProtocol: protocol, ContextTokens: 32768, MaxOutputTokens: 128, Timeout: 30 * time.Second, Transport: policyTransport(func(r *http.Request) (*http.Response, error) {
		policyCalls.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGateway()
	path, env, err := isolatedFiles(dir, base, key, "nexus-fixture", "test-model", 32768, 128)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "/opt/homebrew/bin/openclaw", "agent", "exec", "--config", path, "--cwd", dir, "--model", "nexus-fixture/test-model", "--thinking", "off", "--code-mode", "direct", "--message-file", "-", "--timeout", "30", "--json")
	command.Env = env
	command.Dir = dir
	command.Stdin = strings.NewReader("Return fixture answer.")
	command.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("native exec: %v\n%s", err, stderr.String())
	}
	if violation.Load() || calls.Load() != 1 {
		t.Fatalf("invalid calls=%d violation=%v", calls.Load(), violation.Load())
	}
	projection, err := ParseProjection(stdout.Bytes(), command.ProcessState.ExitCode(), "nexus-fixture", "test-model")
	if err != nil || projection.Text != "fixture answer" {
		t.Fatalf("projection failed: %v\n%s", err, stdout.String())
	}
	completion, err := verified()
	if err != nil || strings.TrimRightFunc(completion.Text, jsWhitespace) != projection.Text || policyCalls.Load() != 1 {
		t.Fatalf("gateway binding failed: %v", err)
	}
	if strings.Contains(stderr.String(), "cannot be represented per model") {
		t.Fatal("context setting discarded")
	}
}
