package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// TestMVPCloudOnlyOpenAICompatible complements the local-only and hybrid
// end-to-end fixtures selected by qualify-mvp. It exercises the production
// OpenAI-compatible transport under cloud-only admission and verifies the
// resulting durable journal rather than treating a provider response as proof.
func TestMVPCloudOnlyOpenAICompatible(t *testing.T) {
	const credential = "mvp-cloud-fixture-credential"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer "+credential {
			t.Error("cloud request path or authentication invalid")
			return
		}
		var request struct {
			Model    string              `json:"model"`
			Stream   bool                `json:"stream"`
			Messages []providers.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Model != "gpt-5.6-sol" || !request.Stream || len(request.Messages) != 1 || request.Messages[0].Content != "cloud qualification" {
			t.Errorf("unexpected cloud request: %+v", request)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cloud-qualified\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Mode = "cloud_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "cloud-only.db")
	cfg.Providers = []config.Provider{{ID: "cloud", Kind: "openai_compatible", Endpoint: server.URL, APIKeyEnv: "MVP_CLOUD_KEY"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "coordinator", Model: "gpt-5.6-sol", Provider: "cloud", Locality: "cloud", ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}}}

	svc, err := NewService(cfg, func(name string) string {
		if name == "MVP_CLOUD_KEY" {
			return credential
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Run(context.Background(), Request{ModelID: "coordinator", Prompt: "cloud qualification", Domain: "qualification"})
	if err != nil || result.Text != "cloud-qualified" || calls.Load() != 1 {
		t.Fatalf("cloud-only execution failed: %+v err=%v calls=%d", result, err, calls.Load())
	}

	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), result.TaskID, 0, 100)
	if err != nil || len(events) < 3 || events[0].Kind != runtime.TaskStarted || events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatalf("cloud-only journal incomplete: events=%v err=%v", events, err)
	}
	for _, event := range events {
		raw, encodeErr := event.Encode()
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if strings.Contains(string(raw), credential) {
			t.Fatal("cloud credential persisted")
		}
	}
}
