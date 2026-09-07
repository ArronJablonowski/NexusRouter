package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

func modelCatalogService() *Service {
	cost := 0.0
	settings := config.Defaults()
	settings.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	settings.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &cost, RAMBytes: 1024}}
	return &Service{settings: settings}
}

func TestConfiguredModelCatalogMapsMaximumConfiguration(t *testing.T) {
	service := modelCatalogService()
	service.settings.Models = make([]config.Model, 256)
	for i := range service.settings.Models {
		suffix := strconv.Itoa(i)
		service.settings.Models[i] = config.Model{ID: "model-" + suffix, Provider: "local", Model: "fixture-" + suffix, Locality: "local", Capabilities: []string{"chat"}}
	}
	if err := service.settings.Validate(); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.ConfiguredModelCatalog(context.Background())
	if err != nil || catalog.Validate() != nil || len(catalog.Models) != 256 || catalog.Models[255].ID != "model-255" {
		t.Fatal("maximum configuration was not mapped", len(catalog.Models), err)
	}
}

func TestConfiguredModelCatalogIsOwnedAndInert(t *testing.T) {
	service := modelCatalogService()
	catalog, err := service.ConfiguredModelCatalog(context.Background())
	if err != nil || catalog.Validate() != nil || len(catalog.Models) != 1 || catalog.Models[0].ID != "chat" || catalog.Models[0].EstimatedCost == nil || *catalog.Models[0].EstimatedCost != 0 {
		t.Fatal(catalog, err)
	}
	redacted, err := service.settings.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(redacted)
	if catalog.ConfigID != hex.EncodeToString(digest[:]) {
		t.Fatal("catalog fingerprint differs from routing configuration fingerprint")
	}
	catalog.Models[0].Capabilities[0] = "mutated"
	*catalog.Models[0].EstimatedCost = 9
	fresh, err := service.ConfiguredModelCatalog(context.Background())
	if err != nil || fresh.Models[0].Capabilities[0] != "chat" || *fresh.Models[0].EstimatedCost != 0 {
		t.Fatal("returned catalog aliases service settings", fresh, err)
	}
}

func TestConfiguredModelCatalogRejectsRotatedCredentialCollision(t *testing.T) {
	service := modelCatalogService()
	service.settings.Models[0].Model = "rotated-secret"
	var calls atomic.Int32
	service.secret = func(string) string {
		if calls.Add(1) > 1 {
			return "rotated-secret"
		}
		return ""
	}
	catalog, err := service.ConfiguredModelCatalog(context.Background())
	if !errors.Is(err, ErrAdmission) || catalog.Version != 0 || calls.Load() < 2 {
		t.Fatal("credential collision escaped", catalog, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.ConfiguredModelCatalog(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConfiguredModelCatalogOmitsProviderSecretsAndEndpoints(t *testing.T) {
	service := modelCatalogService()
	service.settings.Providers[0].APIKeyEnv = "PRIVATE_KEY"
	service.secret = func(string) string { return strings.Repeat("s", 32) }
	catalog, err := service.ConfiguredModelCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if selectionValueClean(catalog, []string{"PRIVATE_KEY", "127.0.0.1:11434"}) == false {
		t.Fatal("catalog exposed provider configuration")
	}
}
