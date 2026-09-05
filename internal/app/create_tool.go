package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func createFileSpec() providers.Tool {
	return providers.Tool{Name: "create_file", Description: "Create a NEW UTF-8 file in the configured local create root after operator approval. Never overwrites or creates parent directories; maximum 64 KiB content.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"content":{"type":"string","maxLength":65536}},"required":["path","content"],"additionalProperties":false}`)}
}

func registerCreateTool(registry *tools.Registry, path string) (func(), string, error) {
	if registry == nil {
		return nil, "", ErrAdmission
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, "", ErrAdmission
	}
	// All built-in file operations share this scope within the durable store.
	// Root-specific identities cannot exclude overlapping/nested roots, aliases,
	// or paths redirected inside an already-pinned root. Parallel reads remain
	// possible; writers conservatively exclude unrelated roots as well.
	scope := "workspace"
	err = registry.Register(tools.Definition{Tool: createFileSpec(), Scope: scope, ReadOnly: false, Behavior: runtime.BehaviorNonIdempotentWrite, Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
		return createNewFile(ctx, root, raw)
	}})
	if err != nil {
		root.Close()
		return nil, "", ErrAdmission
	}
	return func() { root.Close() }, scope, nil
}

func createNewFile(ctx context.Context, root *os.Root, raw json.RawMessage) (out runtime.ToolResult, err error) {
	out = runtime.ToolResult{Content: `{"error":"file_create_unavailable"}`, Effect: runtime.NoEffect, Failed: true}
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if ctx == nil || root == nil || len(raw) > 1<<20 || !utf8.Valid(raw) || json.Unmarshal(raw, &args) != nil || !createPath(args.Path) || !utf8.ValidString(args.Content) || len(args.Content) > 64<<10 || ctx.Err() != nil {
		return out, nil
	}
	// The parent must already exist. Pin it so final publication and directory
	// fsync cannot be redirected by renaming a path component during execution.
	parent, err := root.OpenRoot(filepath.Dir(args.Path))
	if err != nil {
		return out, nil
	}
	defer parent.Close()
	name := filepath.Base(args.Path)
	if name == "." || name == ".." {
		return out, nil
	}
	if _, err = parent.Lstat(name); !os.IsNotExist(err) {
		return out, nil
	}
	stage := ".darwin-create-" + rand.Text()
	if err = parent.Mkdir(stage, 0700); err != nil {
		return out, nil
	}
	// Cleanup touches only our private stage, never the published target. Failed
	// cleanup is an uncertain side effect even if publication never occurred.
	defer func() {
		if e := parent.Remove(stage + "/content"); e != nil && !os.IsNotExist(e) {
			out.Effect = runtime.UncertainEffect
			out.Failed = true
		}
		if e := parent.Remove(stage); e != nil {
			out.Effect = runtime.UncertainEffect
			out.Failed = true
		}
	}()
	file, err := parent.OpenFile(stage+"/content", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return out, nil
	}
	_, writeErr := file.WriteString(args.Content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || ctx.Err() != nil {
		return out, nil
	}
	// Link is an atomic no-replace publication: an existing file, symlink,
	// directory, FIFO or device is never replaced, even after the earlier check.
	if err = parent.Link(stage+"/content", name); err != nil {
		if !os.IsExist(err) {
			out.Effect = runtime.UncertainEffect
		}
		return out, nil
	}
	out.Effect = runtime.ConfirmedEffect
	directory, err := parent.Open(".")
	if err != nil {
		out.Effect = runtime.UncertainEffect
		return out, nil
	}
	syncErr = directory.Sync()
	closeErr = directory.Close()
	if syncErr != nil || closeErr != nil || ctx.Err() != nil {
		out.Effect = runtime.UncertainEffect
		return out, nil
	}
	out.Content = `{"created":true}`
	out.Failed = false
	return out, nil
}

func createPath(path string) bool {
	if !filepath.IsLocal(path) || len(path) > 4096 || strings.ContainsAny(path, "\x00\\") || !utf8.ValidString(path) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range path {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
