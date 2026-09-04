package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"

	"darwinrouter/providers"
	"darwinrouter/runtime"
	"darwinrouter/tools"
)

// readTools binds an operator-selected directory once per task. Model paths
// are relative and cannot escape the directory, including through symlinks.
func readFileSpec() providers.Tool {
	return providers.Tool{Name: "read_file", Description: "Read a UTF-8 regular file within the configured local workspace (maximum 64 KiB).", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096}},"required":["path"],"additionalProperties":false}`)}
}

func readTools(path string) (*tools.Registry, func(), error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, ErrAdmission
	}
	registry := &tools.Registry{}
	err = registry.Register(tools.Definition{
		Tool:  readFileSpec(),
		Scope: "workspace", ReadOnly: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			failed := runtime.ToolResult{Content: `{"error":"file_unavailable"}`, Effect: runtime.NoEffect}
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(raw, &args) != nil || !filepath.IsLocal(args.Path) || ctx.Err() != nil {
				return failed, nil
			}
			// Nonblocking open prevents a FIFO from stalling before the type check.
			file, err := root.OpenFile(args.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return failed, nil
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
				return failed, nil
			}
			content, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
			if err != nil || len(content) > 64<<10 || !utf8.Valid(content) || ctx.Err() != nil {
				return failed, nil
			}
			encoded, _ := json.Marshal(map[string]string{"content": string(content)})
			return runtime.ToolResult{Content: string(encoded), Effect: runtime.NoEffect}, nil
		},
	})
	if err != nil {
		root.Close()
		return nil, nil, ErrAdmission
	}
	return registry, func() { root.Close() }, nil
}
