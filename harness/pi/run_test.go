package pi

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
			Model    string
			Messages []struct {
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
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"nexus-fixture","choices":[{"index":0,"delta":{"role":"assistant","content":"native Pi result"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"nexus-fixture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	cfg := Config{Prices: &Prices{}, Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Provider: "nexus-test", Model: "nexus-fixture", BaseURL: provider.URL + "/v1", APIKey: "fixture-only", ContextTokens: 16384, MaxOutputTokens: 1024, Timeout: 15 * time.Second, Admit: func(context.Context) (func(), error) { return func() { released.Add(1) }, nil }}
	result, e := Run(context.Background(), cfg, "Return a short answer.")
	if e != nil || result.Text != "native Pi result" || result.Provider != "nexus-test" || result.Model != "nexus-fixture" {
		t.Fatal(result, e)
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
	cfg := Config{Prices: &Prices{}, Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Provider: "nexus-test", Model: "nexus-fixture", BaseURL: provider.URL + "/v1", APIKey: "fixture-only", ContextTokens: 16384, MaxOutputTokens: 1024, Timeout: 15 * time.Second, Admit: func(context.Context) (func(), error) { return func() { released.Add(1) }, nil }}
	result, err := Run(ctx, cfg, "Return an answer.")
	if err == nil || result.Text != "" || calls.Load() != 1 || released.Load() != 1 {
		t.Fatal("cancellation returned success, retried, or leaked reservation", result, err, calls.Load(), released.Load())
	}
}
