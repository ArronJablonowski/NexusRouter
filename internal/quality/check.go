// Package quality implements the repository's deterministic source gates.
package quality

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const MaxLines = 1000

// Check inspects all Go source, including tests and untracked files.
// Generated Go files and vendor contents are excluded. It never rewrites files.
func Check(root string) ([]string, error) {
	var findings []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "vendor" || entry.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			findings = append(findings, rel+": symlinked Go source is not supported")
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), rel, data, parser.ParseComments)
		if err != nil {
			findings = append(findings, rel+": invalid Go source")
			return nil
		}
		if ast.IsGenerated(file) {
			return nil
		}
		lines := bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		if lines > MaxLines {
			findings = append(findings, fmt.Sprintf("%s: %d lines exceeds limit %d", rel, lines, MaxLines))
		}
		formatted, err := format.Source(data)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, formatted) {
			findings = append(findings, rel+": run gofmt")
		}
		return nil
	})
	return findings, err
}
