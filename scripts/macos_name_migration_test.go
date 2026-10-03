package scripts

import (
	"os/exec"
	"testing"
)

func TestMacOSNameMigration(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the optional macOS migration utility")
	}
	cmd := exec.Command(python, "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_migrate*.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("migration regression tests: %v\n%s", err, output)
	}
}
