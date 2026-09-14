package releasepack

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestGoReconstructionCreatesFreshPrivateSanitizedWorkspace(t *testing.T) {
	ambientModule := filepath.Join(t.TempDir(), "ambient-module-cache")
	ambientBuild := filepath.Join(t.TempDir(), "ambient-build-cache")
	workspace, err := newGoReconstruction(append(environment(),
		"GOMODCACHE="+ambientModule, "GOCACHE="+ambientBuild,
		"GOPROXY=https://user:secret@example.invalid,direct", "GOSUMDB=off",
		"GOPRIVATE=secret.example", "GONOSUMDB=secret.example", "GOAUTH=netrc",
		"GOPATH="+filepath.Join(t.TempDir(), "ambient-gopath"), "GOFLAGS=-mod=mod",
		"GOWORK=on", "GOTOOLCHAIN=auto", "CGO_ENABLED=1", "AWS_SECRET_ACCESS_KEY=hidden"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := workspace.close(); err != nil {
			t.Fatal(err)
		}
	}()
	if !directoryEmpty(workspace.moduleCache.path) || !directoryEmpty(workspace.buildCache.path) ||
		!directoryEmpty(workspace.goPath.path) || !directoryEmpty(workspace.temporary.path) {
		t.Fatal("reconstruction workspace was not initially empty")
	}
	var config string
	if runtime.GOOS == "darwin" {
		config = filepath.Join(workspace.home.path, "Library", "Application Support")
	} else {
		config = filepath.Join(workspace.home.path, ".config")
	}
	mode, err := os.ReadFile(filepath.Join(config, "go", "telemetry", "mode"))
	if err != nil || string(mode) != "off 2000-01-01\n" {
		t.Fatal("private Go telemetry was not disabled", err)
	}
	joined := strings.Join(workspace.env, "\n")
	for _, forbidden := range []string{ambientModule, ambientBuild, "user:secret", "secret.example", "GOAUTH=netrc", "hidden"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("ambient Go state retained")
		}
	}
	for _, required := range []string{
		"GOMODCACHE=" + workspace.moduleCache.path,
		"GOCACHE=" + workspace.buildCache.path,
		"GOPATH=" + workspace.goPath.path,
		"GOPROXY=" + goReconstructionProxy,
		"GOSUMDB=" + goReconstructionSumDatabase,
		"GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOINSECURE=",
		"GOAUTH=off", "GOVCS=*:off", "GOTELEMETRY=off",
		"GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0",
		"GOENV=off", "GOAMD64=v1", "GOARM64=v8.0", "GOEXPERIMENT=", "GODEBUG=",
	} {
		if !environmentContainsExactly(workspace.env, required) {
			t.Fatal("missing exact reconstruction environment", required)
		}
	}
	if err := workspace.verify(); err != nil {
		t.Fatal(err)
	}
	if !validReconstructionEnvironment(workspace.env, workspace) ||
		validReconstructionEnvironment(append(append([]string(nil), workspace.env...), "GOPROXY=off"), workspace) ||
		validReconstructionEnvironment(append(append([]string(nil), workspace.env...), "TOKEN=secret"), workspace) {
		t.Fatal("reconstruction environment validation is not fail closed")
	}
}

func TestGoReconstructionRejectsProtectedPathOverlap(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	before, err := os.ReadDir(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if workspace, err := newGoReconstruction(environment(), temporary); err == nil || workspace != nil {
		if workspace != nil {
			_ = workspace.close()
		}
		t.Fatal("workspace inside protected path accepted")
	}
	after, err := os.ReadDir(temporary)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("protected temporary parent was modified before overlap rejection", err)
	}
	alias := filepath.Join(t.TempDir(), "temporary-alias")
	if err := os.Symlink(temporary, alias); err != nil {
		t.Fatal(err)
	}
	if workspace, err := newGoReconstruction(environment(), alias); err == nil || workspace != nil {
		if workspace != nil {
			_ = workspace.close()
		}
		t.Fatal("workspace inside symlinked protected path accepted")
	}
}

func TestGoReconstructionDetectsDirectoryIdentityAndModeDrift(t *testing.T) {
	workspace, err := newGoReconstruction(environment(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(workspace.moduleCache.path, 0755); err != nil {
		t.Fatal(err)
	}
	root := workspace.root.path
	if workspace.verify() == nil || workspace.close() == nil {
		t.Fatal("cache mode drift accepted")
	}
	if _, err = os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("drifted private workspace retained")
	}

	workspace, err = newGoReconstruction(environment(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := workspace.buildCache.path
	moved := original + ".moved"
	if err = os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	root = workspace.root.path
	if workspace.verify() == nil || workspace.close() == nil {
		t.Fatal("cache replacement accepted")
	}
	if _, err = os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("replacement private workspace retained")
	}
}

func TestGoReconstructionCleanupDoesNotFollowReplacedChildSymlink(t *testing.T) {
	protected := t.TempDir()
	protectedFile := filepath.Join(protected, "must-survive")
	if err := os.WriteFile(protectedFile, []byte("protected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := newGoReconstruction(environment(), protected)
	if err != nil {
		t.Fatal(err)
	}
	displaced := filepath.Join(workspace.root.path, "displaced-cache")
	if err = os.Rename(workspace.moduleCache.path, displaced); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(displaced, "private-data"), []byte("erase me\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(protected, workspace.moduleCache.path); err != nil {
		t.Fatal(err)
	}
	root := workspace.root.path
	if workspace.close() == nil {
		t.Fatal("replaced cache identity accepted")
	}
	if body, readErr := os.ReadFile(protectedFile); readErr != nil || string(body) != "protected\n" {
		t.Fatal("cleanup followed replacement symlink", readErr)
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Fatal("private cache data retained")
	}
}

func TestGoReconstructionCleanupStaysAnchoredAfterRootReplacement(t *testing.T) {
	protected := t.TempDir()
	protectedFile := filepath.Join(protected, "must-survive")
	if err := os.WriteFile(protectedFile, []byte("protected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := newGoReconstruction(environment(), protected)
	if err != nil {
		t.Fatal(err)
	}
	privateFile := filepath.Join(workspace.moduleCache.path, "private-data")
	if err = os.WriteFile(privateFile, []byte("erase me\n"), 0600); err != nil {
		t.Fatal(err)
	}
	original := workspace.root.path
	displaced := original + ".displaced"
	if err = os.Rename(original, displaced); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(protected, original); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(original)
	defer os.Remove(displaced)
	if workspace.close() == nil {
		t.Fatal("root identity replacement accepted")
	}
	if body, readErr := os.ReadFile(protectedFile); readErr != nil || string(body) != "protected\n" {
		t.Fatal("cleanup followed replacement root symlink", readErr)
	}
	if entries, readErr := os.ReadDir(displaced); readErr != nil || len(entries) != 0 {
		t.Fatal("anchored private cache contents retained", readErr)
	}
}

func TestGoReconstructionCleansReadOnlyModuleDirectories(t *testing.T) {
	workspace, err := newGoReconstruction(environment(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(workspace.moduleCache.path, "example.com", "module@v1.0.0")
	if err = os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(nested, "LICENSE"), []byte("license\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Dir(nested), 0555); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(nested, 0555); err != nil {
		t.Fatal(err)
	}
	root := workspace.root.path
	if err = workspace.close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("private reconstruction workspace retained")
	}
}

func TestCanonicalProspectivePathResolvesMissingSuffixAndSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalProspectivePath(filepath.Join(alias, "missing", "out"))
	real, realErr := filepath.EvalSymlinks(real)
	if err != nil || realErr != nil || got != filepath.Join(real, "missing", "out") {
		t.Fatal("canonical prospective path", got, err)
	}
	if !reconstructionPathsOverlap(real, filepath.Join(real, "child")) || reconstructionPathsOverlap(real, filepath.Join(root, "other")) {
		t.Fatal("path overlap relation incorrect")
	}
}

func environmentContainsExactly(env []string, expected string) bool {
	key, _, _ := strings.Cut(expected, "=")
	count := 0
	for _, value := range env {
		if strings.HasPrefix(value, key+"=") {
			count++
			if value != expected {
				return false
			}
		}
	}
	return count == 1
}
