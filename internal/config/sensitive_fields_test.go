package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSensitiveEnvironmentReferencesValidateAndLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("security:\n  redact_env: [CUSTOM_SECRET, PRIVATE_VALUE_2]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(Options{ProjectFile: path})
	if err != nil || len(loaded.Security.RedactEnv) != 2 || loaded.Security.RedactEnv[0] != "CUSTOM_SECRET" {
		t.Fatalf("sensitive references did not load: %#v %v", loaded.Security.RedactEnv, err)
	}
	s := Defaults()
	s.Security.RedactEnv = []string{"CUSTOM_SECRET", "PRIVATE_VALUE_2"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"bad-name"}, {"DUPLICATE", "DUPLICATE"}, make([]string, 65)} {
		copy := s
		copy.Security.RedactEnv = names
		if err := copy.Validate(); err == nil || strings.Contains(err.Error(), "CUSTOM_SECRET") {
			t.Fatalf("invalid references accepted or disclosed: %v", err)
		}
	}
}
