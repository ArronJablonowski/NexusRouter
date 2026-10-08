package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebApprovalInstalledServiceFromAnyWorkingDirectory(t *testing.T) {
	for _, name := range []string{"commander-config.json", "custom folder/settings.yaml"} {
		t.Run(name, func(t *testing.T) {
			token := strings.Repeat("s", 32)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+token || r.Method != "POST" || r.URL.Path != "/v1/browser-session/challenges/"+strings.Repeat("a", 24)+"/approve" {
					t.Error("wrong approval authority")
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			// The actual listener is an effective service environment override.
			if err := os.WriteFile(path, []byte("version: 1\ndaemon:\n  listen: 127.0.0.1:1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NEXUS_API_TOKEN", "")
			t.Setenv("NEXUS_CONFIG", "")
			discover := func() ([]webInstallation, error) {
				return []webInstallation{{label: "com.nexusrouter.custom", path: path, token: token, env: map[string]string{"daemon.listen": strings.TrimPrefix(server.URL, "http://")}}}, nil
			}
			var out, diag bytes.Buffer
			if code := runWebWithDiscovery([]string{"approve", strings.Repeat("a", 24) + ".12345678"}, &out, &diag, discover); code != 0 || calls != 1 || diag.Len() != 0 {
				t.Fatalf("code=%d calls=%d diagnostic=%s", code, calls, &diag)
			}
			if strings.Contains(out.String(), token) {
				t.Fatal("credential disclosed")
			}
		})
	}
}

func TestWebInstallationSelection(t *testing.T) {
	home := t.TempDir()
	a := webInstallation{label: "com.nexusrouter.a", path: filepath.Join(home, "a.yaml"), token: "a"}
	b := webInstallation{label: "com.nexusrouter.b", path: filepath.Join(home, "b.yaml"), token: "b"}
	discover := func() ([]webInstallation, error) { return []webInstallation{a, b}, nil }
	for _, tc := range []struct {
		name, path, label, token, want string
		fail                           bool
	}{
		{name: "ambiguous", fail: true},
		{name: "explicit service", label: b.label, want: b.path},
		{name: "explicit config", path: a.path, want: a.path},
		{name: "conflicting selectors", path: a.path, label: b.label, fail: true},
		{name: "unknown service", label: "com.nexusrouter.missing", fail: true},
		{name: "explicit authority", path: a.path, token: "explicit", want: a.path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWebInstallation(tc.path, tc.label, tc.token, home, home, nil, discover)
			if (err != nil) != tc.fail || (!tc.fail && got.path != tc.want) {
				t.Fatalf("unexpected selection: path=%s error=%v", got.path, err)
			}
		})
	}
	if _, err := resolveWebInstallation("", "", "explicit", "", "", nil, func() ([]webInstallation, error) { return nil, nil }); err == nil {
		t.Fatal("missing home searched current directory")
	}
	// Explicit authority works without any platform service manager.
	if _, err := resolveWebInstallation(a.path, "", "explicit", home, home, nil, func() ([]webInstallation, error) { t.Fatal("explicit authority probed services"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWebInstallationStandardAndLegacyLocations(t *testing.T) {
	for _, relative := range []string{".NexusRouter/config/config.yaml", ".NexusRouter/config/commander-config.json", ".NexusRouter/config/config.json", "xdg/nexusrouter/config.yaml", "xdg/darwinrouter/config.yaml", ".NexusRouter/data/live-test/config.yaml", "Library/Application Support/NexusRouter/live-test/config.yaml", "Library/Application Support/DarwinRouter/live-test/config.yaml"} {
		t.Run(relative, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{\"version\":1}"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := resolveWebInstallation("", "", "explicit", home, filepath.Join(home, "xdg"), nil, func() ([]webInstallation, error) { return nil, nil })
			if err != nil || got.path != path {
				t.Fatal("location unavailable", err)
			}
		})
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".NexusRouter/config")
	_ = os.MkdirAll(dir, 0700)
	for _, name := range []string{"config.yaml", "commander-config.json"} {
		_ = os.WriteFile(filepath.Join(dir, name), []byte("version: 1\n"), 0600)
	}
	if _, err := resolveWebInstallation("", "", "explicit", home, home, nil, func() ([]webInstallation, error) { return nil, nil }); err == nil {
		t.Fatal("ambiguous files accepted")
	}
}

func TestWebLaunchDiscoveryBoundaries(t *testing.T) {
	token := strings.Repeat("s", 32)
	raw := fmt.Sprintf("gui/501/com.nexusrouter.custom = {\n\targuments = {\n\t\t/custom/bin/nexus\n\t\tserve\n\t\t--config\n\t\tconfig with spaces.json\n\t}\n\tworking directory = /custom install\n\tenvironment = {\n\t\tNEXUS_API_TOKEN => %s\n\t\tNEXUS__DAEMON__LISTEN => 127.0.0.1:8899\n\t}\n}\n", token)
	service, ok, err := parseWebLaunchService("com.nexusrouter.custom", raw)
	if err != nil || !ok || service.path != "/custom install/config with spaces.json" || service.token != token || service.env["daemon.listen"] != "127.0.0.1:8899" {
		t.Fatal("service metadata not resolved")
	}
	for _, replace := range []struct {
		old, new string
		fail     bool
	}{
		{"\t\tserve\n", "\t\tremote\n", false},
		{"\tworking directory = /custom install\n", "", true},
		{"\t\tconfig with spaces.json\n", "\t\tconfig.json\n\t\t--config=other.json\n", true},
	} {
		_, ok, err := parseWebLaunchService(service.label, strings.Replace(raw, replace.old, replace.new, 1))
		if (err != nil) != replace.fail || ok {
			t.Fatal("invalid/foreign service selected")
		}
	}
	spoof := strings.Replace(raw, "NEXUS_API_TOKEN => "+token, "OTHER => NEXUS_API_TOKEN="+token, 1)
	ignored, _, err := parseWebLaunchService(service.label, spoof)
	if err != nil || ignored.token != "" {
		t.Fatal("unrelated environment granted authority")
	}
	calls := 0
	found, err := discoverWebLaunchServices("PID Status Label\n- 0 com.nexusrouter.stopped\n1 0 unrelated\n2 0 com.nexusrouter.custom\n", func(label string) (string, error) { calls++; return raw, nil })
	if err != nil || len(found) != 1 || calls != 1 {
		t.Fatal("unexpected service discovery", err)
	}
	var bounded webLaunchBuffer
	if _, err := bounded.Write(make([]byte, (1<<20)+1)); err == nil {
		t.Fatal("unbounded service output")
	}
}

func TestWebApprovalConfigurationEnvironmentAndFlagPrecedence(t *testing.T) {
	token := strings.Repeat("t", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "router.json")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("{\"version\":1,\"daemon\":{\"listen\":%q}}", strings.TrimPrefix(server.URL, "http://"))), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_API_TOKEN", token)
	t.Setenv("NEXUS_CONFIG", path)
	discover := func() ([]webInstallation, error) {
		t.Fatal("explicit environment queried service manager")
		return nil, nil
	}
	var out, diag bytes.Buffer
	if runWebWithDiscovery([]string{"approve", strings.Repeat("a", 24) + ".12345678"}, &out, &diag, discover) != 0 {
		t.Fatal("environment configuration failed", &diag)
	}
	t.Setenv("NEXUS_CONFIG", "/missing/config.yaml")
	if runWebWithDiscovery([]string{"approve", "--config", path, strings.Repeat("a", 24) + ".12345678"}, &out, &diag, discover) != 0 {
		t.Fatal("explicit flag did not win", &diag)
	}
	if runWebWithDiscovery([]string{"approve", strings.Repeat("a", 24) + ".12345678"}, &out, &diag, discover) != 1 {
		t.Fatal("missing explicit configuration fell back")
	}
}
