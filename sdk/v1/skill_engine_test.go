package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"go.yaml.in/yaml/v3"
)

type sdkSkillEngine struct {
	store                    skills.Store
	mode                     string
	discovers, loads, writes int
}

func (s *sdkSkillEngine) Discover(ctx context.Context, scope string, tags []string, limit int) ([]skills.Metadata, error) {
	s.discovers++
	if scope != "project" || len(tags) != 1 || tags[0] != "general" || limit != 3 {
		panic("unexpected discovery scope")
	}
	out, err := s.store.Discover(ctx, scope, tags, limit)
	if err == nil && len(out) > 0 && s.mode == "scope" {
		out[0].Key.Scope = "foreign"
	}
	return out, err
}
func (s *sdkSkillEngine) Load(ctx context.Context, key skills.Key, version string) (skills.Version, error) {
	s.loads++
	out, err := s.store.Load(ctx, key, version)
	if err == nil && s.mode == "version" {
		out.ID = "wrong-version"
	}
	return out, err
}
func (s *sdkSkillEngine) Draft(context.Context, skills.Draft, bool) (skills.Version, error) {
	s.writes++
	panic("unexpected draft")
}
func (s *sdkSkillEngine) Activate(context.Context, skills.Key, string, string, skills.Validator, bool) error {
	s.writes++
	panic("unexpected activation")
}
func (s *sdkSkillEngine) Rollback(context.Context, skills.Key, string, bool) error {
	s.writes++
	panic("unexpected rollback")
}

func TestSDKSkillEngineScopedReadOnlyWorkflow(t *testing.T) {
	for _, mode := range []string{"valid", "disabled", "scope", "version"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store, err := skills.Open(filepath.Join(root, "store"), []string{"project"})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			draft := skills.Draft{Key: skills.Key{Scope: "project", Name: "workflow"}, Description: "SDK workflow", Tags: []string{"general"}, SourceSessions: []string{"source-session"}, Steps: []string{"Follow the injected workflow"}, ValidationCases: []string{"deterministic fixture"}}
			version, err := store.Draft(ctx, draft, false)
			if err != nil {
				t.Fatal(err)
			}
			validator := skills.ValidatorFunc(func(_ context.Context, v skills.Version) (skills.Evidence, error) {
				return skills.Evidence{ID: "fixture-check", Passed: v.ID == version.ID && v.Draft.Steps[0] == draft.Steps[0], Deterministic: true}, nil
			})
			if err := store.Activate(ctx, draft.Key, version.ID, "", validator, false); err != nil {
				t.Fatal(err)
			}
			wrapper := &sdkSkillEngine{store: store, mode: mode}
			var calls atomic.Int32
			received := make(chan []providers.Message, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []providers.Message `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid provider request")
					return
				}
				calls.Add(1)
				received <- body.Messages
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "answer"}, "done": true, "done_reason": "stop"})
			}))
			defer server.Close()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Hardware.AutoProfile = false
			cfg.Skills.Enabled = mode != "disabled"
			cfg.Skills.Root = filepath.Join(root, "nonexistent-custom-root")
			cfg.Skills.Scope = "project"
			cfg.Telemetry.Database = filepath.Join(root, "tasks.db")
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
			body, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path, SkillStore: wrapper, ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
			if wrapper.writes != 0 {
				t.Fatal("SDK mutated skills", wrapper.writes)
			}
			if _, statErr := os.Stat(cfg.Skills.Root); !os.IsNotExist(statErr) {
				t.Fatal("custom root was created", statErr)
			}
			if mode == "scope" || mode == "version" {
				if err == nil || calls.Load() != 0 || wrapper.discovers != 1 {
					t.Fatal("malicious store admitted", result, err, calls.Load())
				}
				if mode == "version" && wrapper.loads != 1 {
					t.Fatal("version denial never reached load")
				}
				return
			}
			if err != nil || result.Text != "answer" || calls.Load() != 1 {
				t.Fatal(result, err, calls.Load())
			}
			messages := <-received
			snapshot, err := client.InspectTask(ctx, result.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			contextJSON, _ := json.Marshal(messages)
			durableJSON, _ := json.Marshal(snapshot.Messages)
			if mode == "disabled" {
				if wrapper.discovers != 0 || wrapper.loads != 0 || strings.Contains(string(contextJSON), draft.Steps[0]) {
					t.Fatal("disabled skills accessed")
				}
				return
			}
			if wrapper.discovers != 1 || wrapper.loads != 1 || !strings.Contains(string(contextJSON), draft.Steps[0]) || !strings.Contains(string(durableJSON), draft.Steps[0]) || !strings.Contains(string(contextJSON), version.ID) {
				t.Fatal("injected workflow or pinned version missing", string(contextJSON))
			}
		})
	}
}
