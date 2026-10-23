package releasepack

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotExcludesIgnoredAndModifiedWorktree(t *testing.T) {
	repo := t.TempDir()
	env := environment()
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		out, err := command(ctx, repo, env, "git", args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run("init", "-q")
	for name, data := range map[string]string{"go.mod": "module example.com/release\n\ngo 1.27.1\n", "main.go": "package main\n", ".gitignore": "ignored.go\n", ".gitattributes": "main.go export-ignore\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "-qm", "fixture")
	commit := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "ignored.go"), []byte("uncommitted ignored source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := snapshot(ctx, repo, commit, dest, env); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "main.go")); err != nil || string(got) != "package main\n" {
		t.Fatal(string(got), err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ignored.go")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	run("update-index", "--add", "--cacheinfo", "160000,"+commit+",submodule")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "-qm", "gitlink")
	if snapshot(ctx, repo, run("rev-parse", "HEAD"), t.TempDir(), env) == nil {
		t.Fatal("unmaterialized submodule accepted")
	}
}

func TestSnapshotRejectsLocalReplacement(t *testing.T) {
	for _, replacement := range []string{"../outside", "./inside", "/absolute"} {
		dir := t.TempDir()
		body := "module example.com/release\n\ngo 1.27.1\nreplace example.com/dependency => " + replacement + "\n"
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if validateModule(context.Background(), dir, environment()) == nil {
			t.Fatal(replacement)
		}
	}
}

func TestBoundedOutputCannotBypassWriteThroughReadFrom(t *testing.T) {
	var output boundedOutput
	reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", (1<<20)+1))}
	if _, err := io.Copy(&output, reader); err == nil || !output.overflow || output.Len() > 1<<20 {
		t.Fatal("capture cap bypassed", err, output.Len())
	}
}

func TestSnapshotBinaryBlobHasSeparateBoundFromCommandMetadata(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	env := environment()
	git := func(args ...string) string {
		t.Helper()
		out, err := command(ctx, repo, env, "git", args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/release\n\ngo 1.27.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := bytes.Repeat([]byte{0, 1, 2, 255}, (1<<18)+1024)
	if err := os.WriteFile(filepath.Join(repo, "asset.png"), binary, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "large binary")
	dest := t.TempDir()
	if err := snapshot(ctx, repo, git("rev-parse", "HEAD"), dest, env); err != nil {
		t.Fatal("legitimate blob larger than metadata cap rejected", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "asset.png"))
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatal("binary changed", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "asset.png"), make([]byte, maxSnapshotBlobBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "oversized binary")
	if snapshot(ctx, repo, git("rev-parse", "HEAD"), t.TempDir(), env) == nil {
		t.Fatal("oversized blob accepted")
	}
}
