package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"testing"
)

func TestCloudInventoryDiscoveryAndLocalOnly(t *testing.T) {
	s := submissionService(t)
	s.settings.Mode = "hybrid"
	s.settings.Providers = []config.Provider{{ID: "codex", Kind: "codex_app_server", Executable: "fixture"}}
	s.settings.Models = nil
	calls := 0
	s.codexHealthModels = func(context.Context, string) ([]string, error) {
		calls++
		return []string{"cloud-one", "cloud-two", "cloud-one"}, nil
	}
	p := contract.ModelInspectionPage{}
	s.appendCloudInventory(context.Background(), &p)
	if p.CloudDiscoveryStatus != "complete" || len(p.Models) != 2 || calls != 1 {
		t.Fatal(p, calls)
	}
	for _, m := range p.Models {
		if m.Validate() != nil || m.Configured || m.Enabled || m.Usable || m.StatusCode != "catalog_only" {
			t.Fatal(m)
		}
	}
	s.settings.Mode = "local_only"
	p = contract.ModelInspectionPage{}
	s.appendCloudInventory(context.Background(), &p)
	if calls != 1 || len(p.Models) != 0 || p.CloudDiscoveryStatus != "disabled" {
		t.Fatal(p, calls)
	}
}
