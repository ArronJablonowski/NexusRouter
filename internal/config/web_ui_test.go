package config

import "testing"

func TestWebUIConfigurationLayeringAndValidation(t *testing.T) {
	s, err := Load(Options{Env: map[string]string{
		"web_ui.path_prefix":                      "/darwin",
		"web_ui.browser_session_ttl":              "30m",
		"web_ui.model_inventory_refresh_interval": "30s",
	}})
	if err != nil || !s.WebUI.Enabled || s.WebUI.PathPrefix != "/darwin" || s.WebUI.BrowserSessionTTL != "30m" || s.WebUI.ModelInventoryRefreshInterval != "30s" {
		t.Fatal("web UI configuration did not layer", err, s.WebUI)
	}
	for name, mutate := range map[string]func(*Settings){
		"relative path":            func(s *Settings) { s.WebUI.PathPrefix = "app" },
		"reserved path":            func(s *Settings) { s.WebUI.PathPrefix = "/v1" },
		"nested path":              func(s *Settings) { s.WebUI.PathPrefix = "/app/nested" },
		"short ttl":                func(s *Settings) { s.WebUI.BrowserSessionTTL = "1m" },
		"long ttl":                 func(s *Settings) { s.WebUI.BrowserSessionTTL = "25h" },
		"short inventory refresh":  func(s *Settings) { s.WebUI.ModelInventoryRefreshInterval = "4999ms" },
		"long inventory refresh":   func(s *Settings) { s.WebUI.ModelInventoryRefreshInterval = "301s" },
		"sub-ms inventory refresh": func(s *Settings) { s.WebUI.ModelInventoryRefreshInterval = "5000.0001ms" },
		"origin credentials":       func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://user:pass@localhost:7788"} },
		"origin path":              func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://localhost:7788/app"} },
		"origin wrong port":        func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://localhost:7789"} },
		"origin TLS":               func(s *Settings) { s.WebUI.AllowedOrigins = []string{"https://localhost:7788"} },
		"duplicate origin":         func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://localhost:7788", "http://localhost:7788"} },
		"local remote": func(s *Settings) {
			s.Mode = "local_only"
			s.WebUI.AllowedOrigins = []string{"https://example.com"}
		},
		"disabled override": func(s *Settings) {
			s.WebUI.Enabled = false
			s.WebUI.PathPrefix = "/other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := Defaults()
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid web UI configuration accepted")
			}
		})
	}
}

func TestWebUIDisabledDefaultsRemainValid(t *testing.T) {
	s := Defaults()
	s.WebUI.Enabled = false
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWebUIDefaultModelMustReferenceConfiguredModel(t *testing.T) {
	s := Defaults()
	zero := 0.0
	s.Providers = []Provider{{ID: "local", Kind: "ollama"}}
	s.Models = []Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 1024, EstimatedCost: &zero}}
	s.WebUI.DefaultModel = "chat"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WebUI.DefaultModel = "missing"
	if s.Validate() == nil {
		t.Fatal("unknown web UI default model accepted")
	}
	s.WebUI.DefaultModel = "bad model"
	if s.Validate() == nil {
		t.Fatal("invalid web UI default model accepted")
	}
	s.WebUI.DefaultModel = "chat"
	s.WebUI.Enabled = false
	if s.Validate() == nil {
		t.Fatal("disabled web UI retained an active default model")
	}
}
