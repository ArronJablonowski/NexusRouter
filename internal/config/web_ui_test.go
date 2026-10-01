package config

import (
	"strings"
	"testing"
)

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
	s.WebUI.SpecialistsAllowCloud = true
	if s.Validate() == nil {
		t.Fatal("disabled web UI retained cloud specialist routing")
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
	s.WebUI.CommanderFallbackModel = "chat"
	if s.Validate() == nil {
		t.Fatal("commander accepted itself as fallback")
	}
	s.Models = append(s.Models, Model{ID: "backup", Provider: "local", Model: "backup-fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 1024, EstimatedCost: &zero})
	s.WebUI.CommanderFallbackModel = "backup"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WebUI.CommanderFallbackModel = "missing"
	if s.Validate() == nil {
		t.Fatal("unknown commander fallback accepted")
	}
	s.WebUI.CommanderFallbackModel = ""
	s.WebUI.Enabled = false
	if s.Validate() == nil {
		t.Fatal("disabled web UI retained an active default model")
	}
}

func TestWebUIRemoteMembershipOptInPath(t *testing.T) {
	s := Defaults()
	s.WebUI.Enabled = true
	for _, path := range []string{"relative.json", "/private/../peers.json", "/private/peers\n.json"} {
		s.WebUI.RemoteTrustFile = path
		if s.Validate() == nil {
			t.Fatal("accepted", path)
		}
	}
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WebUI.Enabled = false
	if s.Validate() == nil {
		t.Fatal("disabled UI retains trust authority")
	}
}

func TestWebUIRemoteInspectionCredentialsAreExplicit(t *testing.T) {
	s := Defaults()
	s.WebUI.Enabled = true
	s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert.pem", KeyFile: "/private/key.pem", CAFile: "/private/ca.pem"}
	if s.Validate() == nil {
		t.Fatal("credentials without trust")
	}
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WebUI.RemoteClient.KeyFile = ""
	if s.Validate() == nil {
		t.Fatal("partial credentials")
	}
	s.WebUI.RemoteClient.KeyFile = "relative.pem"
	if s.Validate() == nil {
		t.Fatal("relative credentials")
	}
}

func TestWebUIRemoteTaskControlRequiresExplicitClient(t *testing.T) {
	s := Defaults()
	s.WebUI.Enabled = true
	s.WebUI.RemoteTaskControls = true
	if s.Validate() == nil {
		t.Fatal("controls without client")
	}
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert.pem", KeyFile: "/private/key.pem", CAFile: "/private/ca.pem"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WebUI.Enabled = false
	if s.Validate() == nil {
		t.Fatal("disabled UI retains control")
	}
}

func TestRemoteBrowserDispatchOptInAndEvidencePath(t *testing.T) {
	s := Defaults()
	s.WebUI.Enabled = true
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert", KeyFile: "/private/key", CAFile: "/private/ca"}
	s.WebUI.RemoteDispatchDirectory = "/private/routes"
	if s.Validate() == nil {
		t.Fatal("dispatch without task controls")
	}
	s.WebUI.RemoteTaskControls = true
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", "/private/../routes", "/private/route\n"} {
		s.WebUI.RemoteDispatchDirectory = path
		if s.Validate() == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	s.WebUI = Defaults().WebUI
	s.WebUI.RemoteDispatchDirectory = "/private/routes"
	if s.Validate() == nil {
		t.Fatal("disabled UI dispatch authority")
	}
}

func TestAutomaticRemoteBrowserRequiresExplicitEvidenceOptIn(t *testing.T) {
	s := Defaults()
	s.WebUI.RemoteAutomaticEvidenceDirectory = "/private/evidence"
	if s.Validate() == nil {
		t.Fatal("disabled UI authorized automatic routing")
	}
	s.WebUI.Enabled = true
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert", KeyFile: "/private/key", CAFile: "/private/ca"}
	s.WebUI.RemoteTaskControls = true
	if s.Validate() == nil {
		t.Fatal("automatic routing without durable dispatch")
	}
	s.WebUI.RemoteDispatchDirectory = "/private/routes"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", "/private/../evidence", "/private/evidence\n"} {
		s.WebUI.RemoteAutomaticEvidenceDirectory = path
		if s.Validate() == nil {
			t.Fatal(path)
		}
	}
}

func TestRemoteBrowserReviewerNeedsExplicitPolicyAndCost(t *testing.T) {
	s := Defaults()
	zero := 0.0
	s.WebUI.RemoteReview = &WebUIRemoteReview{Model: "reviewer", MaxCost: &zero}
	if s.Validate() == nil {
		t.Fatal("disabled UI review")
	}
	s.WebUI.Enabled = true
	s.WebUI.RemoteTaskControls = true
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert", KeyFile: "/private/key", CAFile: "/private/ca"}
	s.WebUI.RemoteDispatchDirectory = "/private/routes"
	if s.Validate() == nil {
		t.Fatal("review without outcome evidence")
	}
	s.WebUI.RemoteAutomaticEvidenceDirectory = "/private/evidence"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WebUI.RemoteReview.MaxCost = nil
	if s.Validate() == nil {
		t.Fatal("implicit cost allowed")
	}
	negative := -1.0
	s.WebUI.RemoteReview.MaxCost = &negative
	if s.Validate() == nil {
		t.Fatal("negative cost allowed")
	}
	s.WebUI.RemoteReview.MaxCost = &zero
	s.WebUI.RemoteReview.Model = ""
	if s.Validate() == nil {
		t.Fatal("implicit reviewer allowed")
	}
}

func TestRemoteReviewQueueRequiresSeparateAbsoluteStorage(t *testing.T) {
	s := Defaults()
	zero := 0.0
	s.WebUI.Enabled = true
	s.WebUI.RemoteTaskControls = true
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert", KeyFile: "/private/key", CAFile: "/private/ca"}
	s.WebUI.RemoteDispatchDirectory = "/private/routes"
	s.WebUI.RemoteAutomaticEvidenceDirectory = "/private/evidence"
	s.WebUI.RemoteReview = &WebUIRemoteReview{Model: "reviewer", MaxCost: &zero, QueueDirectory: "/private/reviews", Wait: "1h"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"relative", "/private/../reviews", "/private/reviews\n", "/private/routes", "/private/evidence"} {
		s.WebUI.RemoteReview.QueueDirectory = dir
		if s.Validate() == nil {
			t.Fatal("invalid queue accepted", dir)
		}
	}
	s.WebUI.RemoteReview.QueueDirectory = ""
	s.WebUI.RemoteReview.Wait = ""
	if err := s.Validate(); err != nil {
		t.Fatal("manual review requires no queue", err)
	}
}

func TestRemoteReviewWaitRejectsIncompleteOrUnboundedEnrollment(t *testing.T) {
	for _, wait := range []string{"", "0s", "-1s", "25h", "invalid"} {
		w := WebUI{RemoteReview: &WebUIRemoteReview{QueueDirectory: "/private/queue", Wait: wait}}
		// Exercise the duration boundary through a fully configured valid fixture.
		s := Defaults()
		s.WebUI.Enabled = true
		s.WebUI.RemoteTaskControls = true
		s.WebUI.RemoteTrustFile = "/private/peers"
		s.WebUI.RemoteClient = &WebUIRemoteClient{CertificateFile: "/private/cert", KeyFile: "/private/key", CAFile: "/private/ca"}
		s.WebUI.RemoteDispatchDirectory = "/private/routes"
		s.WebUI.RemoteAutomaticEvidenceDirectory = "/private/evidence"
		zero := 0.0
		w.RemoteReview.Model = "reviewer"
		w.RemoteReview.MaxCost = &zero
		s.WebUI.RemoteReview = w.RemoteReview
		if s.Validate() == nil {
			t.Fatal(wait)
		}
		s.WebUI.RemoteReview.Wait = "1h"
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		s.WebUI.RemoteReview.QueueDirectory = ""
		if s.Validate() == nil {
			t.Fatal("wait without queue")
		}
	}
}

func TestRemoteDiscoveryRequiresExplicitMembershipInterface(t *testing.T) {
	s := Defaults()
	s.WebUI.Enabled = true
	s.WebUI.RemoteTrustFile = "/private/peers.json"
	for _, name := range []string{"en0", "eth0", "bridge_1", ""} {
		s.WebUI.RemoteDiscoveryInterface = name
		if err := s.WebUI.Validate("127.0.0.1:9090"); err != nil {
			t.Fatal(name, err)
		}
	}
	for _, name := range []string{"*", "en0\nen1", strings.Repeat("a", 65), " en0"} {
		s.WebUI.RemoteDiscoveryInterface = name
		if err := s.WebUI.Validate("127.0.0.1:9090"); err == nil {
			t.Fatal("accepted", name)
		}
	}
	s.WebUI.RemoteDiscoveryInterface = "en0"
	s.WebUI.RemoteTrustFile = ""
	if err := s.WebUI.Validate("127.0.0.1:9090"); err == nil {
		t.Fatal("discovery without membership")
	}
	s.WebUI.Enabled = false
	if err := s.WebUI.Validate("127.0.0.1:9090"); err == nil {
		t.Fatal("discovery while UI disabled")
	}
}
