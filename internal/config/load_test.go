package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func file(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrecedenceAndExplicitZero(t *testing.T) {
	s, err := Load(Options{
		UserFile:    file(t, "mode: cloud_only\nworkers:\n  max_in_process: 7\nhardware:\n  auto_profile: false\n"),
		ProjectFile: file(t, "mode: local_only\nworkers:\n  max_in_process: 4\nrouting:\n  exploration_rate: 0\n"),
		Env:         map[string]string{"mode": "hybrid", "workers.max_in_process": "2"},
		Flags:       map[string]string{"mode": "local_only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != "local_only" || s.Workers.Max != 2 || s.Hardware.AutoProfile || s.Routing.Exploration != 0 {
		t.Fatalf("bad merge: %+v", s)
	}
	if s.Workers.Lease != "30s" {
		t.Fatal("default sibling lost")
	}
}

func TestRejectedConfigDoesNotLeakValues(t *testing.T) {
	for _, input := range []string{
		"mode: secret-value", "version: 9", "api_key: secret-value", "mode: hybrid\nmode: local_only",
		"mode: null", "mode: &m hybrid\nunknown: *m", "mode: hybrid\n---\nmode: local_only",
		"workers:\n  max_in_process: 0", "workers:\n  heartbeat_interval: 40s",
		"hardware:\n  max_ram_usage_pct: .nan", "routing:\n  weights:\n    quality: -1",
		"mode: local_only\ntelemetry:\n  opentelemetry_enabled: true",
		"providers:\n  - id: cloud\n    kind: openai_compatible\n    endpoint: https://user:secret-value@example.com",
		"providers:\n  - id: cloud\n    kind: openai_compatible\n    endpoint: https://example.com?key=secret-value",
		"models:\n  - id: model\n    provider: missing\n    model: test\n    locality: local\n    capabilities: [chat]",
	} {
		_, err := Load(Options{ProjectFile: file(t, input)})
		if err == nil {
			t.Fatalf("accepted invalid config: %s", input)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatal("secret leaked")
		}
	}
}

func TestListsReplaceAndDisplayRedactsWithoutMutation(t *testing.T) {
	input := "providers:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434/private-secret\n    api_key_env: TEST_API_KEY\n"
	s, err := Load(Options{ProjectFile: file(t, input)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := s.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-secret") || strings.Contains(string(data), "darwin.db") {
		t.Fatal("sensitive settings exposed")
	}
	if !strings.Contains(s.Providers[0].Endpoint, "private-secret") {
		t.Fatal("redaction mutated source")
	}
	s, err = Load(Options{UserFile: file(t, input), ProjectFile: file(t, "providers: []")})
	if err != nil || len(s.Providers) != 0 {
		t.Fatal("list replacement failed", err)
	}
}

func TestEnvironmentAndLiteralOverrides(t *testing.T) {
	e := Environment([]string{"OPENAI_API_KEY=secret", "DARWIN__MODE=local_only", "DARWIN__WORKERS__MAX_IN_PROCESS=2"})
	s, err := Load(Options{Env: e})
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 2 || s.Mode != "local_only" || s.Workers.Max != 2 {
		t.Fatal("bad environment")
	}
	for _, o := range []map[string]string{{"unknown": "secret"}, {"workers": "{}"}, {"mode": "hybrid\napi_key: secret"}} {
		if _, err := Load(Options{Env: o}); err == nil {
			t.Fatal("invalid override accepted")
		}
	}
}

func TestDefaultsDurationAndFileLimits(t *testing.T) {
	if _, err := Load(Options{}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"0d", "99999999999999d", "-1s", "bad"} {
		if _, err := Duration(d); err == nil {
			t.Fatal("invalid duration accepted")
		}
	}
	if _, err := Load(Options{ProjectFile: file(t, strings.Repeat("x", MaxFileBytes+1))}); err == nil {
		t.Fatal("oversized file accepted")
	}
	if _, err := Load(Options{ProjectFile: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("explicit missing file accepted")
	}
}

func TestProviderRequestTimeoutValidation(t *testing.T) {
	for _, timeout := range []string{"100ms", "30s", "5m"} {
		body := "providers:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434\n    request_timeout: " + timeout + "\n"
		settings, err := Load(Options{ProjectFile: file(t, body)})
		if err != nil || len(settings.Providers) != 1 || settings.Providers[0].RequestTimeout != timeout {
			t.Fatalf("valid timeout %q rejected: %+v %v", timeout, settings.Providers, err)
		}
	}
	for _, timeout := range []string{"1ms", "0s", "5m1ns", "invalid"} {
		body := "providers:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434\n    request_timeout: " + timeout + "\n"
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("invalid timeout %q accepted", timeout)
		}
	}
	body := "providers:\n  - id: coordinator\n    kind: codex_app_server\n    executable: /usr/bin/codex\n    request_timeout: 1s\n"
	if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
		t.Fatal("HTTP timeout accepted for subprocess provider")
	}
}

func TestOllamaEndpointDefaultsToPinnedLoopback(t *testing.T) {
	body := "providers:\n  - id: local\n    kind: ollama\n"
	settings, err := Load(Options{ProjectFile: file(t, body)})
	if err != nil || len(settings.Providers) != 1 || settings.Providers[0].Endpoint != "" || settings.Providers[0].ResolvedEndpoint() != DefaultOllamaEndpoint {
		t.Fatalf("default Ollama endpoint unavailable: %+v %v", settings.Providers, err)
	}
	for _, kind := range []string{"openai_compatible", "codex_app_server"} {
		body = "providers:\n  - id: provider\n    kind: " + kind + "\n"
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("missing endpoint/executable accepted for %s", kind)
		}
	}
}
