package openclaw

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The optional path belongs to the qualification operator, never task input.
func nativeExecutable(t *testing.T) string {
	t.Helper()
	path := os.Getenv("NEXUS_OPENCLAW_EXECUTABLE")
	if path == "" {
		var err error
		path, err = exec.LookPath("openclaw")
		if err != nil {
			t.Fatal("native OpenClaw executable required", err)
		}
	}
	if !filepath.IsAbs(path) {
		t.Fatal("native OpenClaw executable must be absolute")
	}
	return path
}
