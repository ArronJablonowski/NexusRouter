package remote

import (
	"os"
	"testing"
)

// Process ownership is deliberately a lifetime singleton. Keep the private
// fixture directory until all tests finish; per-test deletion invalidates later
// dispatchers in this same process and must not be repaired by production code.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "nexus-remote-test-owners-")
	if err != nil {
		os.Exit(1)
	}
	if os.Setenv("NEXUS_PROCESS_OWNER_DIR", root) != nil || os.Setenv("DARWIN_PROCESS_OWNER_DIR", root) != nil {
		os.RemoveAll(root)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}
