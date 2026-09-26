package app

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloudContextTierSkipsLocalCapacity(t *testing.T) {
	model := config.Model{Locality: "cloud", ContextTokens: 272000, DefaultContextTokens: 16384}
	fits := func(int) bool { t.Fatal("cloud used local capacity probe"); return false }
	got, err := chooseContextTier(context.Background(), model, Request{}, 100, nil, false, 1, fits)
	if err != nil || got != 272000 {
		t.Fatalf("cloud tier %d: %v", got, err)
	}
	if _, err = chooseContextTier(context.Background(), model, Request{}, 272001, nil, false, 1, fits); err == nil {
		t.Fatal("provider limit bypassed")
	}
	got, err = chooseContextTier(context.Background(), model, Request{ContextTokens: 64000}, 100, nil, false, 1, fits)
	if err != nil || got != 64000 {
		t.Fatal("explicit per-request limit lost")
	}
}

func TestCloudCatalogRefreshesProviderRecommendation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	svc := modelCatalogService()
	svc.settings.Providers = []config.Provider{{ID: "cloud", Kind: "codex_app_server", Executable: "/fixture/codex"}}
	svc.settings.Models[0].Provider = "cloud"
	svc.settings.Models[0].Model = "gpt-5.6-sol"
	svc.settings.Models[0].Locality = "cloud"
	for _, want := range []int{272000, 300000} {
		data, _ := json.Marshal(map[string]any{"fetched_at": time.Now(), "models": []any{map[string]any{"slug": "gpt-5.6-sol", "context_window": want}}})
		if err := os.WriteFile(filepath.Join(home, "models_cache.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := svc.ConfiguredModelCatalog(context.Background())
		if err != nil || got.Models[0].ContextTokens != want {
			t.Fatalf("catalog did not refresh: %+v %v", got, err)
		}
	}
	if svc.settings.Models[0].ContextTokens != 8192 {
		t.Fatal("shared configuration mutated")
	}
}
