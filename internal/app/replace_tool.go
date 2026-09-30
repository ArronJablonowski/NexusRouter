package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func replaceFileSpec() providers.Tool {
	return providers.Tool{Name: "replace_file", Description: "Replace an existing UTF-8 file after operator review of exact previous and new content. Both contents must be at most 64 KiB and differ. Fails if the previous content changed. Retains a private recovery copy; preserves permission bits, not extended metadata. Only cooperating writers are fenced.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"expected_content":{"type":"string","maxLength":65536},"content":{"type":"string","maxLength":65536}},"required":["path","expected_content","content"],"additionalProperties":false}`)}
}

func registerReplaceTool(registry *tools.Registry, path string) (func(), string, error) {
	if registry == nil {
		return nil, "", ErrAdmission
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, "", ErrAdmission
	}
	const scope = "workspace"
	err = registry.Register(tools.Definition{Tool: replaceFileSpec(), Scope: scope, ReadOnly: false, Behavior: runtime.BehaviorNonIdempotentWrite, Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
		return replaceExistingFile(ctx, root, raw)
	}})
	if err != nil {
		root.Close()
		return nil, "", ErrAdmission
	}
	return func() { root.Close() }, scope, nil
}

// Replacement is atomic visibility, not a filesystem compare-and-swap. The
// dispatcher must hold its writer lease and consume exact per-call approval.
// External writers must cooperate; no portable rename fences them after the
// final preimage check. Retained private backups permit operator recovery.
func replaceExistingFile(ctx context.Context, root *os.Root, raw json.RawMessage) (out runtime.ToolResult, err error) {
	out = runtime.ToolResult{Content: `{"error":"file_replace_unavailable"}`, Effect: runtime.NoEffect, Failed: true}
	args, err := replaceArguments(raw)
	if err != nil || ctx == nil || root == nil || ctx.Err() != nil {
		return out, nil
	}
	parent, err := root.OpenRoot(filepath.Dir(args.Path))
	if err != nil {
		return out, nil
	}
	defer parent.Close()
	name := filepath.Base(args.Path)
	original, err := inspectReplaceSource(ctx, parent, name, args.ExpectedContent)
	if err != nil {
		return out, nil
	}
	stage := ".darwin-replace-" + rand.Text()
	if parent.Mkdir(stage, 0700) != nil {
		return out, nil
	}
	retain := false
	defer func() {
		if retain {
			return
		}
		for _, path := range []string{stage + "/content", stage + "/original", stage} {
			if e := parent.Remove(path); e != nil && !os.IsNotExist(e) {
				out.Effect, out.Failed = runtime.UncertainEffect, true
			}
		}
	}()
	if writeReplacementStage(parent, stage+"/original", args.ExpectedContent, 0600) != nil || writeReplacementStage(parent, stage+"/content", args.Content, original.Mode().Perm()) != nil || syncReplacementDirectory(parent, stage) != nil || syncReplacementDirectory(parent, ".") != nil || ctx.Err() != nil {
		return out, nil
	}
	current, err := inspectReplaceSource(ctx, parent, name, args.ExpectedContent)
	if err != nil || !os.SameFile(original, current) || original.Mode() != current.Mode() || ctx.Err() != nil {
		return out, nil
	}
	// Preserve the original bytes after any publication attempt, including an
	// uncertain filesystem error. Never "repair" a failed rename by replaying it.
	retain = true
	backup := filepath.ToSlash(filepath.Join(filepath.Dir(args.Path), stage, "original"))
	content, _ := json.Marshal(map[string]any{"error": "file_replace_uncertain", "backup": backup})
	out.Content, out.Effect = string(content), runtime.UncertainEffect
	if parent.Rename(stage+"/content", name) != nil {
		return out, nil
	}
	if syncReplacementDirectory(parent, stage) != nil || syncReplacementDirectory(parent, ".") != nil || ctx.Err() != nil {
		return out, nil
	}
	content, _ = json.Marshal(map[string]any{"replaced": true, "backup": backup})
	out.Content, out.Effect, out.Failed = string(content), runtime.ConfirmedEffect, false
	return out, nil
}

func inspectReplaceSource(ctx context.Context, parent *os.Root, name, expected string) (os.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&^os.ModePerm != 0 || before.Size() > 64<<10 || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	file, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrAdmission
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Mode() != before.Mode() || opened.Size() != before.Size() {
		return nil, ErrAdmission
	}
	body, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	after, statErr := file.Stat()
	if readErr != nil || statErr != nil || !os.SameFile(opened, after) || opened.Mode() != after.Mode() || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || !utf8.Valid(body) || string(body) != expected || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	return before, nil
}

func writeReplacementStage(parent *os.Root, path, content string, mode os.FileMode) error {
	file, err := parent.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrAdmission
	}
	n, writeErr := file.WriteString(content)
	modeErr := file.Chmod(mode)
	syncErr := file.Sync()
	closeErr := file.Close()
	if n != len(content) || writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil {
		return ErrAdmission
	}
	return nil
}

func syncReplacementDirectory(parent *os.Root, path string) error {
	dir, err := parent.Open(path)
	if err != nil {
		return ErrAdmission
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil || closeErr != nil {
		return ErrAdmission
	}
	return nil
}

func replaceArguments(raw []byte) (args struct{ Path, ExpectedContent, Content string }, err error) {
	if len(raw) > 1<<20 || !utf8.Valid(raw) {
		return args, ErrAdmission
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return args, ErrAdmission
	}
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		name, ok := key.(string)
		if e != nil || !ok || seen[name] || (name != "path" && name != "expected_content" && name != "content") {
			return args, ErrAdmission
		}
		seen[name] = true
		value, e := d.Token()
		text, ok := value.(string)
		if e != nil || !ok {
			return args, ErrAdmission
		}
		switch name {
		case "path":
			args.Path = text
		case "expected_content":
			args.ExpectedContent = text
		case "content":
			args.Content = text
		}
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return args, ErrAdmission
	}
	if _, e := d.Token(); e != io.EOF || len(seen) != 3 || !createPath(args.Path) || len(args.ExpectedContent) > 64<<10 || len(args.Content) > 64<<10 || args.ExpectedContent == args.Content {
		return args, ErrAdmission
	}
	return args, nil
}
