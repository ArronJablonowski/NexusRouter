package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func replaceTestArgs(path, before, after string) json.RawMessage {
	body, _ := json.Marshal(map[string]string{"path": path, "expected_content": before, "content": after})
	return body
}

func TestReplaceToolExactPreimageBackupAndMode(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0644, 0755} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "nested", "file.txt")
			before := "original 世界\n"
			after := "replacement\n"
			if err := os.WriteFile(path, []byte(before), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			out, err := replaceExistingFile(context.Background(), root, replaceTestArgs("nested/file.txt", before, after))
			if err != nil || out.Failed || out.Effect != runtime.ConfirmedEffect {
				t.Fatal(out, err)
			}
			var receipt struct {
				Replaced bool   `json:"replaced"`
				Backup   string `json:"backup"`
			}
			if err = json.Unmarshal([]byte(out.Content), &receipt); err != nil || !receipt.Replaced || !strings.HasPrefix(receipt.Backup, "nested/.darwin-replace-") || !strings.HasSuffix(receipt.Backup, "/original") {
				t.Fatal(out.Content, err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != after {
				t.Fatal("replacement bytes", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("mode", info, err)
			}
			backup := filepath.Join(dir, receipt.Backup)
			body, err = os.ReadFile(backup)
			if err != nil || string(body) != before {
				t.Fatal("backup bytes", err)
			}
			info, err = os.Stat(filepath.Dir(backup))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("backup not private", err)
			}
			info, err = os.Stat(backup)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("backup file not private", err)
			}
			stale, err := replaceExistingFile(context.Background(), root, replaceTestArgs("nested/file.txt", before, "third"))
			if err != nil || !stale.Failed || stale.Effect != runtime.NoEffect {
				t.Fatal(stale, err)
			}
			body, _ = os.ReadFile(backup)
			if string(body) != before {
				t.Fatal("stale attempt changed backup")
			}
		})
	}
}

func TestReplaceToolInvalidInputsLeaveOriginalUntouched(t *testing.T) {
	cases := []json.RawMessage{
		replaceTestArgs("file", "stale", "new"), replaceTestArgs("file", "old", "old"),
		replaceTestArgs("file", strings.Repeat("x", 65537), "new"), replaceTestArgs("file", "old", strings.Repeat("界", 21846)),
		[]byte(`{"path":"file","expected_content":"old"}`), []byte(`{"path":"file","expected_content":"old","content":null}`),
		[]byte(`{"path":"file","expected_content":"old","content":"new","extra":true}`),
		[]byte(`{"path":"file","path":"other","expected_content":"old","content":"new"}`),
		[]byte(`{"path":"file","expected_content":"old","content":"new"} {}`),
		[]byte{'{', 0xff, '}'},
	}
	for _, path := range []string{"../escape", "/absolute", "missing/file", "a/../file", ".", "a\\b", "a\nfile"} {
		cases = append(cases, replaceTestArgs(path, "old", "new"))
	}
	for i, raw := range cases {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			out, err := replaceExistingFile(context.Background(), root, raw)
			if err != nil || !out.Failed || out.Effect != runtime.NoEffect || out.Content != `{"error":"file_replace_unavailable"}` {
				t.Fatal(out, err)
			}
			body, _ := os.ReadFile(path)
			entries, _ := os.ReadDir(dir)
			if string(body) != "old" || len(entries) != 1 {
				t.Fatal("failed replacement mutated root")
			}
		})
	}
}

func TestReplaceToolSpecialTargetsAndCancellation(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "fifo", "missing", "oversized", "invalid_utf8", "canceled", "nil_context"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("outside", path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "missing":
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", 65537)), 0600)
			case "invalid_utf8":
				err = os.WriteFile(path, []byte{0xff}, 0600)
			default:
				err = os.WriteFile(path, []byte("old"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			ctx := context.Background()
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if kind == "nil_context" {
				ctx = nil
			}
			out, err := replaceExistingFile(ctx, root, replaceTestArgs("file", "old", "new"))
			if err != nil || !out.Failed || out.Effect != runtime.NoEffect {
				t.Fatal(out, err)
			}
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".darwin-replace-") {
					t.Fatal("staging leaked")
				}
			}
		})
	}
}

func TestReplaceToolPinnedRoot(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "root")
	moved := filepath.Join(parent, "moved")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := replaceExistingFile(context.Background(), root, replaceTestArgs("file", "old", "new"))
	if err != nil || out.Failed {
		t.Fatal(out, err)
	}
	current, _ := os.ReadFile(filepath.Join(dir, "file"))
	pinned, _ := os.ReadFile(filepath.Join(moved, "file"))
	if string(current) != "old" || string(pinned) != "new" {
		t.Fatal("root descriptor was not pinned")
	}
}
