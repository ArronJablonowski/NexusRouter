package v1_test

import (
	"context"
	"errors"
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

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

func TestSDKTextStreamLiveRedactionAndNilSink(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const secret = "sdk-text-stream-secret"
	prefix := strings.Repeat("safe answer ", 32)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", prefix)
		w.(http.Flusher).Flush()
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		case <-release:
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", secret[:10])
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", secret[10:]+" complete")
	}))
	defer func() { cancel(); server.Close() }()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Workers.Max = 2
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "SDK_TEXT_SECRET"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{
		ProjectFile: path,
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) {
			return sdkGoodMeasurement(), nil
		}),
		LookupSecret: func(name string) string {
			if name == "SDK_TEXT_SECRET" {
				return secret
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"}
	result, err := client.RunTextStream(ctx, req, nil)
	if !errors.Is(err, sdk.ErrAdmission) || result.Version != 1 || result.TaskID != "" || calls.Load() != 0 {
		t.Fatal("nil sink performed execution", result, err, calls.Load())
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nil sink created storage", err)
	}
	var streamed strings.Builder
	chunks := 0
	result, err = client.RunTextStream(ctx, req, func(chunk string) error {
		if chunk == "" {
			t.Error("empty text callback")
		}
		streamed.WriteString(chunk)
		chunks++
		if chunks == 1 {
			// Completion is gated on an actual text callback, proving this
			// SDK method forwards live text instead of splitting a final result.
			close(release)
		}
		return nil
	})
	want := prefix + "[REDACTED] complete"
	if err != nil || result.Version != 1 || result.TaskID == "" || result.FinishReason != "stop" || result.Text != want || streamed.String() != want || calls.Load() != 1 || chunks < 2 {
		t.Fatal("unexpected live result", result, err, streamed.String(), calls.Load(), chunks)
	}
	if strings.Contains(streamed.String(), secret) {
		t.Fatal("credential exposed")
	}
}
