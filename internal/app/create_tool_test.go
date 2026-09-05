package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestCreateToolNewOnlyAndConfinement(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	run := func(path, content string) runtime.ToolResult {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"path": path, "content": content})
		out, err := createNewFile(context.Background(), root, raw)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := run("new.txt", "héllo\n"); got.Effect != runtime.ConfirmedEffect || got.Content != `{"created":true}` {
		t.Fatal(got)
	}
	if got := run("new.txt", "replace"); got.Effect != runtime.NoEffect {
		t.Fatal(got)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "new.txt")); err != nil || string(data) != "héllo\n" {
		t.Fatal(string(data), err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../escape.txt", "/absolute", "escape/outside.txt", "missing/new.txt", "a/../new.txt", ".", "a\\b", "a\nfile"} {
		if got := run(path, "no"); got.Effect != runtime.NoEffect {
			t.Fatal(path, got)
		}
	}
	if got := run("huge", strings.Repeat("x", (64<<10)+1)); got.Effect != runtime.NoEffect {
		t.Fatal(got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".darwin-create-") {
			t.Fatal("stage leaked")
		}
	}
}

func TestCreateConcurrentPublicationExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	start := make(chan struct{})
	results := make(chan runtime.ToolResult, 2)
	var wg sync.WaitGroup
	for _, content := range []string{"first", "second"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			<-start
			raw, _ := json.Marshal(map[string]string{"path": "shared", "content": content})
			out, err := createNewFile(context.Background(), root, raw)
			if err != nil {
				t.Error(err)
			}
			results <- out
		}(content)
	}
	close(start)
	wg.Wait()
	close(results)
	confirmed, none := 0, 0
	for out := range results {
		switch out.Effect {
		case runtime.ConfirmedEffect:
			confirmed++
		case runtime.NoEffect:
			none++
		default:
			t.Fatal(out)
		}
	}
	if confirmed != 1 || none != 1 {
		t.Fatal(confirmed, none)
	}
	data, err := os.ReadFile(filepath.Join(dir, "shared"))
	if err != nil || (string(data) != "first" && string(data) != "second") {
		t.Fatal(string(data), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("staging artifacts", entries, err)
	}
}

func TestCreatePreservesSpecialTargets(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = os.WriteFile(filepath.Join(dir, "original"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("original", filepath.Join(dir, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"symlink", "directory", "fifo"} {
		before, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"path": name, "content": "replacement"})
		out, err := createNewFile(context.Background(), root, raw)
		if err != nil || out.Effect != runtime.NoEffect {
			t.Fatal(name, out, err)
		}
		after, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			t.Fatal("special target replaced", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "original")); err != nil || string(data) != "retained" {
		t.Fatal(string(data), err)
	}
}

func TestCreateCancellationEmptyAndPrivateMode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := createNewFile(ctx, root, json.RawMessage(`{"path":"canceled","content":"no"}`))
	if err != nil || out.Effect != runtime.NoEffect {
		t.Fatal(out, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	out, err = createNewFile(context.Background(), root, json.RawMessage(`{"path":"empty","content":""}`))
	if err != nil || out.Effect != runtime.ConfirmedEffect {
		t.Fatal(out, err)
	}
	info, err := os.Stat(filepath.Join(dir, "empty"))
	if err != nil || info.Size() != 0 || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestCreateScopeMatchesSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	closeA, scopeA, err := registerCreateTool(&tools.Registry{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeA()
	closeB, scopeB, err := registerCreateTool(&tools.Registry{}, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer closeB()
	if scopeA != scopeB {
		t.Fatal("aliases can evade scope lease", scopeA, scopeB)
	}
}

func TestCreateRootPinAndStableScope(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "root")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	r1, r2 := &tools.Registry{}, &tools.Registry{}
	close1, scope1, err := registerCreateTool(r1, path)
	if err != nil {
		t.Fatal(err)
	}
	defer close1()
	close2, scope2, err := registerCreateTool(r2, path)
	if err != nil {
		t.Fatal(err)
	}
	defer close2()
	if scope1 != scope2 || strings.Contains(scope1, path) {
		t.Fatal("unstable/private scope", scope1, scope2)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	moved := filepath.Join(parent, "moved")
	if err = os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	out, err := createNewFile(context.Background(), root, json.RawMessage(`{"path":"new","content":"pinned"}`))
	if err != nil || out.Effect != runtime.ConfirmedEffect {
		t.Fatal(out, err)
	}
	if _, err = os.Stat(filepath.Join(path, "new")); !os.IsNotExist(err) {
		t.Fatal("reopened replaced root", err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, "new")); err != nil || string(data) != "pinned" {
		t.Fatal(err)
	}
}
