package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareOutcomeEvidenceValidatesWithoutQualityRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	for range 2 {
		if err := PrepareOutcomeEvidence(root); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("preflight left records", entries, err)
	}
	st, err := os.Stat(root)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal(st, err)
	}
	for _, kind := range []string{"public", "file", "symlink", "relative", "missing_parent"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "evidence")
			var err error
			switch kind {
			case "public":
				err = os.Mkdir(path, 0755)
			case "file":
				err = os.WriteFile(path, []byte("existing evidence"), 0600)
			case "symlink":
				err = os.Symlink(root, path)
			case "relative":
				path = "relative-evidence"
			case "missing_parent":
				path = filepath.Join(path, "child")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = PrepareOutcomeEvidence(path); err == nil {
				t.Fatal("invalid root accepted")
			}
		})
	}
}
