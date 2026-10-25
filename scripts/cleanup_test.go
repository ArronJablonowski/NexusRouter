package scripts

import (
	"os/exec"
	"testing"
)

func TestNexusHomeCleanup(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required to verify maintenance safety")
	}
	cmd := exec.Command(python, "-B", "-m", "unittest", "discover", "-s", ".", "-p", "test_cleanup*.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cleanup regression tests: %v\n%s", err, output)
	}
}
