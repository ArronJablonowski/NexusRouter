package routing

import (
	"math"
	"strings"
	"testing"
)

func catalogFixture() ModelCatalog {
	cost := 0.0
	return ModelCatalog{Version: 1, ConfigID: strings.Repeat("a", 64), Models: []ConfiguredModel{{Version: 1, ID: "local-fast", Provider: "ollama", Model: "qwen:latest", Locality: "local", Capabilities: []string{"chat", "code"}, ContextTokens: 8192, EstimatedCost: &cost, RAMBytes: 1024, VRAMBytes: 512, GPUDevice: "nvidia:0", FailureDomain: "host-a"}}}
}

func TestConfiguredModelCatalogValidation(t *testing.T) {
	valid := catalogFixture()
	if valid.Validate() != nil {
		t.Fatal(valid)
	}
	for _, mutate := range []func(*ModelCatalog){
		func(c *ModelCatalog) { c.ConfigID = strings.Repeat("A", 64) },
		func(c *ModelCatalog) { c.Models = nil },
		func(c *ModelCatalog) { c.Models[0].Model = " bad" },
		func(c *ModelCatalog) { c.Models[0].Locality = "remote" },
		func(c *ModelCatalog) { c.Models[0].Capabilities = []string{"chat", "chat"} },
		func(c *ModelCatalog) { value := math.NaN(); c.Models[0].EstimatedCost = &value },
		func(c *ModelCatalog) { c.Models[0].GPUDevice = "private\ndevice" },
		func(c *ModelCatalog) { c.Models = append(c.Models, c.Models[0]) },
	} {
		catalog := catalogFixture()
		mutate(&catalog)
		if catalog.Validate() == nil {
			t.Fatal("invalid catalog accepted", catalog)
		}
	}
}
