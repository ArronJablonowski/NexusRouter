package evaluation

import (
	"strings"
	"testing"
)

func TestGoSourceSyntaxOnly(t *testing.T) {
	for _, source := range []string{"package main\nfunc main() {}", "package p\nvar x = unknownSymbol", "package p\nimport _ \"unavailable.example/pkg\""} {
		if !GoSourceValid(source) {
			t.Fatal("valid syntax rejected", source)
		}
	}
	for _, source := range []string{"", "   ", "func missingPackage() {}", "package p\nfunc broken( {", "```go\npackage p\n```", "Here is your code:\npackage p", "package p\n//\xff", strings.Repeat(" ", (1<<20)+1)} {
		if GoSourceValid(source) {
			t.Fatal("invalid syntax accepted")
		}
	}
}
