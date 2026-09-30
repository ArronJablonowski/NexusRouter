package app

import (
	"context"
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

const (
	maxReadFileBytes      = 64 << 10
	maxCountedEntries     = 100_000
	maxCountedDirectories = 10_000
	maxCountedDepth       = 64
)

type readFileArguments struct {
	Path      string `json:"path"`
	Operation string `json:"operation,omitempty"`
}

type directoryCountResult struct {
	Evidence                 string `json:"evidence"`
	Path                     string `json:"path"`
	NonRecursiveRegularFiles int    `json:"non_recursive_regular_files"`
	RecursiveRegularFiles    int    `json:"recursive_regular_files"`
	DirectoriesVisited       int    `json:"directories_visited"`
	SymlinksSkipped          int    `json:"symlinks_skipped"`
	OtherEntriesSkipped      int    `json:"other_entries_skipped"`
}

// readTools binds an operator-selected directory once per task. Model paths
// are relative and cannot escape the directory, including through symlinks.
func readFileSpec() providers.Tool {
	return providers.Tool{Name: "read_file", Description: "Read a UTF-8 regular file within the configured local workspace (maximum 64 KiB), or return bounded aggregate regular-file counts for a directory without exposing names or contents. Use operation=count_regular_files for directory counts; omit operation to read a file.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"operation":{"type":"string","enum":["count_regular_files"]}},"required":["path"],"additionalProperties":false}`)}
}

func delegatedCountSpec() providers.Tool {
	return providers.Tool{Name: "read_file", Description: "Return bounded non-recursive and recursive regular-file counts for a directory within the configured local workspace. This delegated cloud-coordinator capability exposes no file names or contents.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"operation":{"type":"string","enum":["count_regular_files"]}},"required":["path","operation"],"additionalProperties":false}`)}
}

func readTools(path string) (*tools.Registry, func(), error) {
	return filesystemReadTools(path, false)
}

func delegatedCountTools(path string) (*tools.Registry, func(), error) {
	return filesystemReadTools(path, true)
}

func filesystemReadTools(path string, countOnly bool) (*tools.Registry, func(), error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, ErrAdmission
	}
	registry := &tools.Registry{}
	spec := readFileSpec()
	if countOnly {
		spec = delegatedCountSpec()
	}
	err = registry.Register(tools.Definition{
		Tool:  spec,
		Scope: "workspace", ReadOnly: true, Behavior: runtime.BehaviorReadOnly,
		Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
			failed := runtime.ToolResult{Content: `{"error":"file_unavailable"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
			var args readFileArguments
			if json.Unmarshal(raw, &args) != nil || !filepath.IsLocal(args.Path) || ctx.Err() != nil {
				return failed, nil
			}
			if args.Operation == "count_regular_files" {
				return countRegularFiles(ctx, root, args.Path), nil
			}
			if countOnly {
				return failed, nil
			}
			// Nonblocking open prevents a FIFO from stalling before the type check.
			file, err := root.OpenFile(args.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return failed, nil
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > maxReadFileBytes {
				return failed, nil
			}
			content, err := io.ReadAll(io.LimitReader(file, maxReadFileBytes+1))
			if err != nil || len(content) > maxReadFileBytes || !utf8.Valid(content) || ctx.Err() != nil {
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

func countRegularFiles(ctx context.Context, root *os.Root, path string) runtime.ToolResult {
	failed := runtime.ToolResult{Content: `{"error":"directory_unavailable"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
	info, err := root.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return failed
	}
	type pendingDirectory struct {
		path  string
		depth int
	}
	queue := []pendingDirectory{{path: path}}
	result := directoryCountResult{Evidence: "bounded_root_count_v1", Path: path}
	entriesSeen := 0
	for len(queue) > 0 {
		if ctx.Err() != nil || result.DirectoriesVisited >= maxCountedDirectories {
			return failed
		}
		current := queue[0]
		queue = queue[1:]
		if current.depth > maxCountedDepth {
			return failed
		}
		directory, err := root.Open(current.path)
		if err != nil {
			return failed
		}
		opened, statErr := directory.Stat()
		if statErr != nil || !opened.IsDir() {
			_ = directory.Close()
			return failed
		}
		result.DirectoriesVisited++
		for {
			remaining := maxCountedEntries - entriesSeen
			batchSize := min(256, remaining+1)
			entries, readErr := directory.ReadDir(batchSize)
			if len(entries) > remaining || readErr != nil && readErr != io.EOF {
				_ = directory.Close()
				return failed
			}
			entriesSeen += len(entries)
			for _, entry := range entries {
				if ctx.Err() != nil {
					_ = directory.Close()
					return failed
				}
				child := filepath.Join(current.path, entry.Name())
				childInfo, err := root.Lstat(child)
				if err != nil {
					_ = directory.Close()
					return failed
				}
				if childInfo.Mode()&os.ModeSymlink != 0 {
					result.SymlinksSkipped++
					continue
				}
				switch {
				case childInfo.Mode().IsRegular():
					result.RecursiveRegularFiles++
					if current.depth == 0 {
						result.NonRecursiveRegularFiles++
					}
				case childInfo.IsDir():
					queue = append(queue, pendingDirectory{path: child, depth: current.depth + 1})
				default:
					result.OtherEntriesSkipped++
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		if directory.Close() != nil {
			return failed
		}
	}
	body, err := json.Marshal(result)
	if err != nil || len(body) > maxReadFileBytes {
		return failed
	}
	return runtime.ToolResult{Content: string(body), Effect: runtime.NoEffect}
}
