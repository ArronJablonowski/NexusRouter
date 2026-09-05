package evaluation

import (
	"go/parser"
	"go/token"
	"unicode/utf8"
)

// GoSourceValid checks one raw, complete Go source file without loading imports,
// resolving symbols, compiling, running code, or evaluating build directives.
// It deliberately does not extract fenced snippets or accept prose wrappers.
// A syntactically valid file may still fail type checks, tests, or user intent.
func GoSourceValid(text string) bool {
	if len(text) == 0 || len(text) > 1<<20 || !utf8.ValidString(text) {
		return false
	}
	_, err := parser.ParseFile(token.NewFileSet(), "output.go", text, parser.SkipObjectResolution)
	return err == nil
}
