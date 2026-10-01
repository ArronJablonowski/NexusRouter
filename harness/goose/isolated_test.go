package goose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "real-secret-must-not-inherit")
	t.Setenv("GOOSE_PROVIDER", "must-not-inherit")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	env, err := isolatedEnvironment(dir, "http://127.0.0.1:1234/v1", "child-key", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range env {
		if strings.Contains(v, "must-not-inherit") {
			t.Fatal("inherited live setting")
		}
	}
	for _, u := range []string{"https://127.0.0.1:1234/v1", "http://localhost:1234/v1", "http://127.0.0.1/v1", "http://127.0.0.1:1234/v1?q=x"} {
		if _, err := isolatedEnvironment(dir, u, "key", "fixture"); err == nil {
			t.Fatal("non-gateway accepted")
		}
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedEnvironment(link, "http://127.0.0.1:1234/v1", "key", "fixture"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedEnvironment(dir, "http://127.0.0.1:1234/v1", "key", "fixture"); err == nil {
		t.Fatal("nonempty directory reused")
	}
}
