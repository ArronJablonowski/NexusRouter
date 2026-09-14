package releasepack

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageRoutesEveryGoOperationThroughOneOwnedReconstruction(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "package.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "Package" {
			body = function.Body
			break
		}
	}
	if body == nil {
		t.Fatal("Package function missing")
	}
	counts := map[string]int{}
	badGoEnvironment := false
	buildEnvironmentOwned := false
	ast.Inspect(body, func(node ast.Node) bool {
		if assignment, ok := node.(*ast.AssignStmt); ok && len(assignment.Lhs) == 1 &&
			identifierName(assignment.Lhs[0]) == "buildEnv" && len(assignment.Rhs) == 1 {
			buildEnvironmentOwned = containsIdentifier(assignment.Rhs[0], "reconstructionEnv") &&
				!containsIdentifier(assignment.Rhs[0], "env")
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calledIdentifier(call.Fun)
		counts[name]++
		switch name {
		case "newGoReconstruction":
			if !identifierArguments(call.Args, "env", "source", "out", "stage", "buildSource") {
				badGoEnvironment = true
			}
		case "discoverSBOMSourceFiles":
			if len(call.Args) != 3 || identifierName(call.Args[2]) != "reconstructionEnv" {
				badGoEnvironment = true
			}
		case "targetNoticeClosure":
			if len(call.Args) != 5 || identifierName(call.Args[4]) != "reconstruction" {
				badGoEnvironment = true
			}
		case "targetNoticeModules":
			badGoEnvironment = true
		case "command":
			if len(call.Args) >= 4 && identifierName(call.Args[3]) == "goExecutable" {
				counts["goCommand"]++
				environment := identifierName(call.Args[2])
				if environment != "reconstructionEnv" && environment != "buildEnv" {
					badGoEnvironment = true
				}
			}
		}
		return true
	})
	if counts["newGoReconstruction"] != 1 || counts["discoverSBOMSourceFiles"] != 1 ||
		counts["targetNoticeClosure"] != 1 || counts["goCommand"] != 4 || !buildEnvironmentOwned || badGoEnvironment {
		t.Fatalf("Package reconstruction routing changed: counts=%v bad_environment=%t", counts, badGoEnvironment)
	}
	if counts["close"] == 0 || counts["verify"] < 3 {
		t.Fatalf("Package no longer closes and repeatedly verifies its reconstruction: counts=%v", counts)
	}
	source, err := os.ReadFile("package.go")
	if err != nil {
		t.Fatal(err)
	}
	closeBeforePublish := bytes.LastIndex(source, []byte("if err = reconstruction.close(); err != nil"))
	closedMarker := bytes.LastIndex(source, []byte("reconstructionClosed = true"))
	publish := bytes.LastIndex(source, []byte("return publish(stage, out)"))
	if closeBeforePublish < 0 || closedMarker <= closeBeforePublish || publish <= closedMarker {
		t.Fatal("Package must close its reconstruction successfully before publishing artifacts")
	}
}

func TestPackageCallsReceiveDistinctFreshModuleAndBuildCaches(t *testing.T) {
	protected := t.TempDir()
	first, err := newGoReconstruction(environment(), protected)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if first != nil {
			_ = first.close()
		}
	}()
	if err = os.WriteFile(filepath.Join(first.moduleCache.path, "first-call-sentinel"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(first.buildCache.path, "first-call-sentinel"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := newGoReconstruction(environment(), protected)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if second != nil {
			_ = second.close()
		}
	}()
	if first.root.path == second.root.path || first.moduleCache.path == second.moduleCache.path ||
		first.buildCache.path == second.buildCache.path || pathsOverlap(first.root.path, second.root.path) {
		t.Fatal("separate Package reconstruction calls shared cache identity")
	}
	if !directoryEmpty(second.moduleCache.path) || !directoryEmpty(second.buildCache.path) {
		t.Fatal("later Package reconstruction inherited earlier cache contents")
	}
	if err = first.verify(); err != nil {
		t.Fatal(err)
	}
	if err = second.verify(); err != nil {
		t.Fatal(err)
	}
	if err = first.close(); err != nil {
		t.Fatal(err)
	}
	first = nil
	if err = second.verify(); err != nil {
		t.Fatal("closing one Package reconstruction affected another", err)
	}
}

func calledIdentifier(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func identifierName(expression ast.Expr) string {
	identifier, _ := expression.(*ast.Ident)
	if identifier == nil {
		return ""
	}
	return identifier.Name
}

func identifierArguments(arguments []ast.Expr, names ...string) bool {
	if len(arguments) != len(names) {
		return false
	}
	for i, name := range names {
		if identifierName(arguments[i]) != name {
			return false
		}
	}
	return true
}

func containsIdentifier(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		identifier, ok := current.(*ast.Ident)
		if ok && identifier.Name == name {
			found = true
			return false
		}
		return !found
	})
	return found
}
