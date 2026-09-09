package config

import "testing"

func TestWebUIConfigurationLayeringAndValidation(t *testing.T) {
	s, err := Load(Options{Env: map[string]string{
		"web_ui.path_prefix":         "/darwin",
		"web_ui.browser_session_ttl": "30m",
	}})
	if err != nil || !s.WebUI.Enabled || s.WebUI.PathPrefix != "/darwin" || s.WebUI.BrowserSessionTTL != "30m" {
		t.Fatal("web UI configuration did not layer", err, s.WebUI)
	}
	for name, mutate := range map[string]func(*Settings){
		"relative path":      func(s *Settings) { s.WebUI.PathPrefix = "app" },
		"reserved path":      func(s *Settings) { s.WebUI.PathPrefix = "/v1" },
		"nested path":        func(s *Settings) { s.WebUI.PathPrefix = "/app/nested" },
		"short ttl":          func(s *Settings) { s.WebUI.BrowserSessionTTL = "1m" },
		"long ttl":           func(s *Settings) { s.WebUI.BrowserSessionTTL = "25h" },
		"origin credentials": func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://user:pass@localhost:7788"} },
		"origin path":        func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://localhost:7788/app"} },
		"origin wrong port":  func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://localhost:7789"} },
		"origin TLS":         func(s *Settings) { s.WebUI.AllowedOrigins = []string{"https://localhost:7788"} },
		"duplicate origin":   func(s *Settings) { s.WebUI.AllowedOrigins = []string{"http://localhost:7788", "http://localhost:7788"} },
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
