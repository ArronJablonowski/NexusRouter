//go:build darwin || linux

package releasepack

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExclusiveRehearsalWriteOverridesRestrictiveUmask(t *testing.T) {
	if os.Getenv("NEXUSROUTER_REHEARSAL_UMASK_HELPER") == "1" {
		syscall.Umask(0077)
		path := filepath.Join(t.TempDir(), "darwin")
		if err := writeExclusive(path, []byte("synthetic binary"), 0755); err != nil {
			t.Fatal(err)
		}
		assertMode(t, path, 0755)
		if body, err := os.ReadFile(path); err != nil || string(body) != "synthetic binary" {
			t.Fatal("exclusive write changed content", err)
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestExclusiveRehearsalWriteOverridesRestrictiveUmask$")
	command.Env = append(os.Environ(), "NEXUSROUTER_REHEARSAL_UMASK_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restrictive-umask subprocess failed: %v\n%s", err, output)
	}
}
