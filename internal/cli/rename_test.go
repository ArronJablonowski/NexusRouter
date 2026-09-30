package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestRenameRedactsBothAPITokenNames(t *testing.T) {
	secrets := map[string]string{"NEXUS_API_TOKEN": "canonical-secret", "DARWIN_API_TOKEN": "legacy-secret"}
	values := logSecrets(config.Settings{}, func(name string) string { return secrets[name] })
	found := map[string]bool{}
	for _, value := range values {
		found[value] = true
	}
	if !found["canonical-secret"] || !found["legacy-secret"] {
		t.Fatal("both credential names must be protected")
	}
}

func TestRenameConfigDiscoveryPrefersNewAndRetainsLegacy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project.yaml")
	if err := os.WriteFile(project, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		t.Helper()
		path := filepath.Join(base, name, "config.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	show := func(want string) {
		t.Helper()
		var out, diag bytes.Buffer
		code := runConfig([]string{"show", "--config", project}, &out, &diag)
		if code != 0 || !strings.Contains(out.String(), want) {
			t.Fatalf("config discovery: code=%d out=%s diag=%s", code, &out, &diag)
		}
	}
	write("darwinrouter", "workboard:\n  decomposition:\n    max_depth: 3\n")
	show(`"max_depth": 3`)
	write("nexusrouter", "workboard:\n  decomposition:\n    max_depth: 4\n")
	show(`"max_depth": 4`)
}
