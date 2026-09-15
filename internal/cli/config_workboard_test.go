package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigShowIncludesEffectiveWorkboardDecompositionLimits(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.yaml")
	project := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(user, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte("workboard:\n  decomposition:\n    max_depth: 3\n    max_children_per_parent: 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runConfig([]string{"show", "--config", project, "--user-config", user,
		"--set", "workboard.decomposition.max_children_per_parent=5"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"version": 1`) || !strings.Contains(stdout.String(), `"max_depth": 3`) ||
		!strings.Contains(stdout.String(), `"max_children_per_parent": 5`) {
		t.Fatalf("effective decomposition display failed: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}
