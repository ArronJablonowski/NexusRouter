package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

type sdkMemoryEngine struct {
	memory.Store
	mode    string
	queries int
	query   memory.Query
	writes  int
}

func (s *sdkMemoryEngine) QueryMemory(_ context.Context, q memory.Query) ([]memory.Fact, error) {
	s.queries++
	s.query = q
	if s.mode == "error" {
		return nil, errors.New("private store error")
	}
	if s.mode == "panic" {
		panic("private store panic")
	}
	fact := memory.Fact{Version: 1, ID: "fact", Scope: "project", Revision: 1, Content: "useful sdk-memory-secret fact", Provenance: "operator", Confidence: .8, Privacy: "shareable", Created: time.Unix(100, 0), Updated: time.Unix(100, 0)}
	switch s.mode {
	case "scope":
		fact.Scope = "foreign"
	case "private":
		fact.Privacy = "local_only"
	case "expired":
		fact.Expires = time.Now().Add(-time.Hour)
	case "version":
		fact.Version = 2
	}
	return []memory.Fact{fact}, nil
}
func (s *sdkMemoryEngine) PutMemory(context.Context, memory.Fact, int64) error {
	s.writes++
	return errors.New("unexpected write")
}
func (s *sdkMemoryEngine) TouchMemory(context.Context, string, string, time.Time) error {
	s.writes++
	return errors.New("unexpected write")
}
func (s *sdkMemoryEngine) DeleteMemory(context.Context, string, string, int64) error {
	s.writes++
	return errors.New("unexpected write")
}
func (s *sdkMemoryEngine) ExpireMemory(context.Context, string, time.Time) (int64, error) {
	s.writes++
	return 0, errors.New("unexpected write")
}

func TestSDKMemoryEngineScopedReadOnlyAndUntrusted(t *testing.T) {
	for _, mode := range []string{"valid", "disabled", "scope", "private", "expired", "version", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var calls atomic.Int32
			received := make(chan []providers.Message, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []providers.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "bad", 400)
					return
				}
				calls.Add(1)
				received <- body.Messages
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "answer"}, "done": true, "done_reason": "stop"})
			}))
			defer server.Close()
			defer cancel()
			store := &sdkMemoryEngine{mode: mode}
			cfg := config.Defaults()
			cfg.Mode = "hybrid"
			cfg.Hardware.AutoProfile = false
			cfg.Memory.Enabled = mode != "disabled"
			cfg.Memory.Scope = "project"
			cfg.Memory.LocalOnly = false
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "SDK_MEMORY_SECRET"}}
			locality := "local"
			if mode == "private" {
				locality = "cloud"
			}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: locality, RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
			body, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path, MemoryStore: store, ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }), LookupSecret: func(name string) string {
				if name == "SDK_MEMORY_SECRET" {
					return "sdk-memory-secret"
				}
				return ""
			}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
			if store.writes != 0 {
				t.Fatal("memory mutated", store.writes)
			}
			if mode != "valid" && mode != "disabled" {
				if err == nil || calls.Load() != 0 || strings.Contains(err.Error(), "private") {
					t.Fatal(result, err)
				}
				if store.queries != 1 {
					t.Fatal("custom store not consulted", store.queries)
				}
				if mode == "private" && store.query.LocalOnly {
					t.Fatal("cloud query enabled private facts")
				}
				return
			}
			if err != nil || result.Text != "answer" || calls.Load() != 1 {
				t.Fatal(result, err)
			}
			messages := <-received
			if mode == "disabled" {
				if store.queries != 0 || len(messages) != 1 {
					t.Fatal("disabled memory queried", store.queries, messages)
				}
				return
			}
			if store.queries != 1 || store.query.Scope != "project" || store.query.Contains != "" || store.query.IncludeExpired || store.query.Now.IsZero() || store.query.Limit != cfg.Memory.MaxFacts {
				t.Fatal(store.query)
			}
			if len(messages) != 3 || messages[0].Role != "system" || !strings.Contains(messages[0].Content, "untrusted factual context") {
				t.Fatal(messages)
			}
			var envelope struct {
				Facts []map[string]any `json:"memory_facts"`
			}
			if json.Unmarshal([]byte(messages[1].Content), &envelope) != nil || len(envelope.Facts) != 1 || len(envelope.Facts[0]) != 5 || strings.Contains(messages[1].Content, "sdk-memory-secret") {
				t.Fatal("unsafe memory envelope", messages[1])
			}
			snapshot, err := client.InspectTask(ctx, result.TaskID)
			if err != nil || len(snapshot.Messages) != 4 || snapshot.Messages[1].Content != messages[1].Content {
				t.Fatal("memory not durably preserved", snapshot, err)
			}
		})
	}
}
