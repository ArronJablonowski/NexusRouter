package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOllamaInstalledModelsIncludesBoundedStorageMetadata(t *testing.T) {
	digest := strings.Repeat("a", 64)
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" && r.Method == http.MethodPost {
			_, _ = fmt.Fprint(w, `{"model_info":{"qwen.context_length":32768}}`)
			return
		}
		if r.URL.Path != "/api/tags" || r.Method != http.MethodGet {
			t.Errorf("unexpected inventory request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"models":[{"name":"qwen:latest","modified_at":"2026-09-17T12:00:00Z","size":4294967296,"digest":%q,"details":{"family":"qwen","parameter_size":"7B","quantization_level":"Q4_K_M"}}]}`, digest)
	})
	models, err := p.InstalledModels(context.Background())
	if err != nil || len(models) != 1 || models[0].Name != "qwen:latest" || models[0].SizeBytes != 4294967296 || models[0].Digest != digest || models[0].Quantization != "Q4_K_M" || models[0].ContextTokens != 32768 {
		t.Fatalf("unexpected inventory: %+v, %v", models, err)
	}
}

func TestOllamaInstalledModelsAllowsNonTextModelWithoutContext(t *testing.T) {
	digest := strings.Repeat("a", 64)
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_, _ = fmt.Fprint(w, `{"model_info":{}}`)
			return
		}
		fmt.Fprintf(w, `{"models":[{"name":"qwen:latest","size":1,"digest":%q}]}`, digest)
	})
	if models, err := p.InstalledModels(context.Background()); err != nil || len(models) != 1 || models[0].ContextTokens != 0 {
		t.Fatalf("non-text model inventory rejected: %+v, %v", models, err)
	}
}

func TestOllamaInstalledModelsRejectsUnverifiableSizeAndDigest(t *testing.T) {
	for name, body := range map[string]string{
		"missing size": `{"models":[{"name":"qwen:latest","digest":"` + strings.Repeat("a", 64) + `"}]}`,
		"zero size":    `{"models":[{"name":"qwen:latest","size":0,"digest":"` + strings.Repeat("a", 64) + `"}]}`,
		"bad digest":   `{"models":[{"name":"qwen:latest","size":1,"digest":"private"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) })
			if models, err := p.InstalledModels(context.Background()); err == nil || models != nil {
				t.Fatalf("unverifiable inventory accepted: %+v", models)
			}
		})
	}
}

func TestOllamaInventoryStorageSurvivesOptionalContextFailure(t *testing.T) {
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			http.Error(w, "unsupported", 500)
			return
		}
		fmt.Fprintf(w, `{"models":[{"name":"muse:latest","modified_at":"2026-09-17T12:00:00-06:00","size":123456789,"digest":%q}]}`, strings.Repeat("a", 64))
	})
	models, err := p.InstalledModels(context.Background())
	if err != nil || len(models) != 1 || models[0].SizeBytes != 123456789 || models[0].ModifiedAt.Location() != time.UTC || models[0].ModifiedAt.Hour() != 18 {
		t.Fatalf("storage lost: %+v %v", models, err)
	}
}
