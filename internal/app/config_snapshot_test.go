package app

import "testing"

func TestServiceOwnsNestedConfigurationSnapshot(t *testing.T) {
	svc, cfg := autoFixture(t)
	wantEndpoint := svc.settings.Providers[0].Endpoint
	wantContext := svc.settings.Models[0].ContextTokens
	cfg.Providers[0].Endpoint = "https://unapproved.invalid"
	cfg.Models[0].ContextTokens = 999999
	cfg.Models[0].Capabilities[0] = "write_files"
	*cfg.Models[0].EstimatedCost = 999
	if svc.settings.Providers[0].Endpoint != wantEndpoint || svc.settings.Models[0].ContextTokens != wantContext || svc.settings.Models[0].Capabilities[0] != "chat" || *svc.settings.Models[0].EstimatedCost != 0 {
		t.Fatal("caller mutation changed active admission configuration")
	}
	svc.settings.Models[1].Capabilities[0] = "internal_change"
	if cfg.Models[1].Capabilities[0] != "chat" {
		t.Fatal("service mutation changed caller configuration")
	}
}
