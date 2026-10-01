package cli

import (
	"bytes"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagedRemoteCLIUsesSuppliedIOAndExplicitTrust(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if code := RunWithInput([]string{"remote", "help"}, strings.NewReader(""), &output, &diagnostics, "test"); code != 0 || !strings.Contains(output.String(), "auto-dispatch") || diagnostics.Len() != 0 {
		t.Fatal(code, output.String(), diagnostics.String())
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "trust.json")
	output.Reset()
	if code := RunWithInput([]string{"remote", "replace-trust", "--trust", path, "--expected", "absent"}, strings.NewReader(`{"version":1,"peers":[]}`), &output, &diagnostics, "test"); code != 0 {
		t.Fatal(code, diagnostics.String())
	}
	registry, e := remote.TrustFile(path).Read()
	if e != nil || strings.TrimSpace(output.String()) != registry.Digest() {
		t.Fatal(registry, e, output.String())
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal(st, e)
	}
	output.Reset()
	if code := RunWithInput([]string{"remote", "validate-trust", "--trust", path}, strings.NewReader(""), &output, &diagnostics, "test"); code != 0 || strings.TrimSpace(output.String()) != registry.Digest() {
		t.Fatal(code, output.String(), diagnostics.String())
	}
	output.Reset()
	diagnostics.Reset()
	if code := RunWithInput([]string{"remote", "info", "--unknown-option"}, strings.NewReader(""), &output, &diagnostics, "test"); code != 1 || diagnostics.Len() == 0 || output.Len() != 0 {
		t.Fatal(code, output.String(), diagnostics.String())
	}
	diagnostics.Reset()
	if code := RunWithInput([]string{"remote", "info", "--help"}, strings.NewReader(""), &output, &diagnostics, "test"); code != 0 || !strings.Contains(diagnostics.String(), "trust") {
		t.Fatal(code, diagnostics.String())
	}
}
