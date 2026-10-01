package openhands

import (
	"bytes"
	"context"
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
)

func TestNativeSDKBridge(t *testing.T) {
	python := os.Getenv("NEXUS_OPENHANDS_PYTHON")
	if python == "" {
		t.Skip("set NEXUS_OPENHANDS_PYTHON to the pinned SDK interpreter")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-only" || request["stream"] != true || request["tools"] != nil || request["max_completion_tokens"] != float64(128) {
			t.Error("unexpected native request")
			http.Error(w, "invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{"role":"assistant","content":"native OpenHands fixture"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	bridge, err := filepath.Abs("bridge.py")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	body, _ := json.Marshal(map[string]any{"base_url": server.URL + "/v1", "api_key": "fixture-only", "model": "openai/fixture-model", "workspace": workspace, "max_output_tokens": 128, "context_tokens": 32768, "timeout_seconds": 20})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", bridge)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + workspace, "OPENHANDS_SUPPRESS_BANNER=1", "LITELLM_LOCAL_MODEL_COST_MAP=True"}
	cmd.Dir = workspace
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("native bridge: %v %s", err, stderr.String())
	}
	p, err := ParseProjection(output, 0, "openai/fixture-model")
	if err != nil || p.Text != "native OpenHands fixture" || calls.Load() != 1 {
		t.Fatal(p, err, calls.Load())
	}
}
