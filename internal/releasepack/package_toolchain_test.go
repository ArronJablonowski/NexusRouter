package releasepack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedReleaseExecutableRejectsReplacement(t *testing.T) {
	makeExecutable := func(t *testing.T) (string, []string) {
		t.Helper()
		directory := t.TempDir()
		path := filepath.Join(directory, "go")
		if err := os.WriteFile(path, []byte("first\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return path, []string{"PATH=" + directory}
	}

	t.Run("unchanged", func(t *testing.T) {
		path, env := makeExecutable(t)
		identity, err := pinReleaseExecutable(env, "go")
		expected, resolveErr := filepath.EvalSymlinks(path)
		if err != nil || resolveErr != nil || identity.path != expected || identity.verify() != nil {
			t.Fatal("unchanged executable rejected", identity.path, err)
		}
	})

	t.Run("renamed_replacement", func(t *testing.T) {
		path, env := makeExecutable(t)
		identity, err := pinReleaseExecutable(env, "go")
		if err != nil {
			t.Fatal(err)
		}
		old := path + ".old"
		if err = os.Rename(path, old); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("replacement\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if identity.verify() == nil {
			t.Fatal("different executable at pinned path accepted")
		}
	})

	t.Run("permissions_changed", func(t *testing.T) {
		path, env := makeExecutable(t)
		identity, err := pinReleaseExecutable(env, "go")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		if identity.verify() == nil {
			t.Fatal("non-executable pinned file accepted")
		}
	})
}

func TestReleaseExecutablePATHIsRestrictedForNoticeGeneration(t *testing.T) {
	executable := filepath.Join(string(filepath.Separator), "toolchain", "bin", "go")
	env := executableOnlyPATH([]string{"HOME=/tmp/home", "PATH=/first:/second", "GOENV=off"}, executable)
	if len(env) != 3 || env[0] != "HOME=/tmp/home" || env[1] != "GOENV=off" || env[2] != "PATH="+filepath.Dir(executable) {
		t.Fatal("notice PATH was not restricted to the pinned executable", env)
	}
	for _, value := range env {
		if strings.Contains(value, "/first") || strings.Contains(value, "/second") {
			t.Fatal("ambient executable search path retained", env)
		}
	}
}
