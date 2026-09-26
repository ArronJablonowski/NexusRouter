package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloudRecommendationsRefreshAndKeepLocalLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	s := Settings{Providers: []Provider{{ID: "codex", Kind: "codex_app_server"}, {ID: "other", Kind: "openai_compatible"}}, Models: []Model{{ID: "cloud", Provider: "codex", Model: "sol", Locality: "cloud", ContextTokens: 16384}, {ID: "local", Provider: "codex", Model: "sol", Locality: "local", ContextTokens: 65536}, {ID: "other", Provider: "other", Model: "sol", Locality: "cloud", ContextTokens: 99000}}}
	for _, window := range []int{272000, 300000} {
		data, _ := json.Marshal(map[string]any{"fetched_at": time.Now(), "models": []map[string]any{{"slug": "sol", "context_window": window, "max_context_window": 872000}}})
		if err := os.WriteFile(filepath.Join(home, "models_cache.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		got := WithCloudContextRecommendations(s)
		if got.Models[0].ContextTokens != window || got.Models[0].WorkingContextTokens() != window || got.Models[1].ContextTokens != 65536 || got.Models[1].WorkingContextTokens() != 32768 || got.Models[2].ContextTokens != 99000 || s.Models[0].ContextTokens != 16384 {
			t.Fatalf("incorrect recommendation snapshot: %+v", got.Models)
		}
	}
}

func TestCodexRecommendationRejectsInvalidCatalog(t *testing.T) {
	now := time.Now().UTC()
	for _, data := range []string{`{`, `{"models":[]}`, `{"fetched_at":"2000-01-01T00:00:00Z","models":[{"slug":"sol","context_window":272000}]}`} {
		if got := codexRecommendedWindows([]byte(data), now); got != nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	for _, models := range []any{
		[]any{map[string]any{"slug": "sol", "context_window": -1}},
		[]any{map[string]any{"slug": "sol", "context_window": 272000}, map[string]any{"slug": "sol", "context_window": 300000}},
		[]any{map[string]any{"slug": "sol", "max_context_window": 872000}},
	} {
		data, _ := json.Marshal(map[string]any{"fetched_at": now, "models": models})
		if codexRecommendedWindows(data, now) != nil {
			t.Fatal("invalid model metadata accepted")
		}
	}
}

func TestCloudWorkingWindowIgnoresLocalStartingTier(t *testing.T) {
	m := Model{Locality: "cloud", ContextTokens: 272000, DefaultContextTokens: 16384}
	if m.WorkingContextTokens() != 272000 {
		t.Fatal("cloud reduced to local starting tier")
	}
}
