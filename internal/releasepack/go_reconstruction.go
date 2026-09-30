package releasepack

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	goReconstructionPolicy      = "nexusrouter-public-go-reconstruction-v1"
	goReconstructionProxy       = "https://proxy.golang.org"
	goReconstructionSumDatabase = "sum.golang.org"
)

type goReconstructionPolicyOptions struct {
	policy           string
	moduleProxy      string
	checksumDatabase string
}

func productionGoReconstructionPolicy() goReconstructionPolicyOptions {
	return goReconstructionPolicyOptions{
		policy: goReconstructionPolicy, moduleProxy: goReconstructionProxy, checksumDatabase: goReconstructionSumDatabase,
	}
}

// goReconstruction owns one initially empty, private Go module/build cache.
// Paths are process-local implementation details and never enter evidence.
type goReconstruction struct {
	root         pinnedReconstructionDirectory
	anchor       *os.Root
	moduleCache  pinnedReconstructionDirectory
	buildCache   pinnedReconstructionDirectory
	goPath       pinnedReconstructionDirectory
	home         pinnedReconstructionDirectory
	temporary    pinnedReconstructionDirectory
	goExecutable releaseExecutableIdentity
	policy       goReconstructionPolicyOptions
	env          []string
}

type pinnedReconstructionDirectory struct {
	path string
	info os.FileInfo
}

// newGoReconstruction creates all cache state itself. It never accepts an
// existing cache and fails when the new workspace overlaps a protected path.
func newGoReconstruction(baseEnv []string, protectedPaths ...string) (*goReconstruction, error) {
	return newGoReconstructionWithPolicy(baseEnv, productionGoReconstructionPolicy(), protectedPaths...)
}

func newGoReconstructionWithPolicy(baseEnv []string, policy goReconstructionPolicyOptions, protectedPaths ...string) (*goReconstruction, error) {
	if !validGoReconstructionPolicy(policy) {
		return nil, ErrInvalid
	}
	goExecutable, err := pinReleaseExecutable(baseEnv, "go")
	if err != nil || goExecutable.verify() != nil {
		return nil, ErrInvalid
	}
	canonicalProtected := make([]string, 0, len(protectedPaths))
	for _, protected := range protectedPaths {
		canonical, canonicalErr := canonicalProspectivePath(protected)
		if canonicalErr != nil {
			return nil, ErrInvalid
		}
		canonicalProtected = append(canonicalProtected, canonical)
	}
	// MkdirTemp creates the directory in os.TempDir when its dir argument is
	// empty. Reject a protected parent before creation so even a short-lived
	// reconstruction directory is never placed in protected state.
	temporaryParent, err := canonicalProspectivePath(os.TempDir())
	if err != nil {
		return nil, ErrInvalid
	}
	for _, protected := range canonicalProtected {
		if pathContains(protected, temporaryParent) {
			return nil, ErrInvalid
		}
	}
	root, err := os.MkdirTemp("", ".darwin-go-reconstruct-")
	if err != nil {
		return nil, ErrInvalid
	}
	var workspace *goReconstruction
	keep := false
	anchoredCleanup := false
	defer func() {
		if !keep && !anchoredCleanup {
			_ = os.RemoveAll(root)
		}
	}()
	root, err = filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) {
		return nil, ErrInvalid
	}
	for _, protected := range canonicalProtected {
		if reconstructionPathsOverlap(root, protected) {
			return nil, ErrInvalid
		}
	}
	workspace = &goReconstruction{goExecutable: goExecutable, policy: policy}
	workspace.root, err = pinReconstructionDirectory(root)
	if err != nil {
		return nil, ErrInvalid
	}
	workspace.anchor, err = os.OpenRoot(root)
	if err != nil {
		return nil, ErrInvalid
	}
	anchoredCleanup = true
	defer func() {
		if !keep && workspace.anchor != nil {
			_ = workspace.cleanup()
		}
	}()
	for name, destination := range map[string]*pinnedReconstructionDirectory{
		"gomodcache": &workspace.moduleCache,
		"gocache":    &workspace.buildCache,
		"gopath":     &workspace.goPath,
		"home":       &workspace.home,
		"tmp":        &workspace.temporary,
	} {
		path := filepath.Join(root, name)
		if err = os.Mkdir(path, 0700); err != nil {
			return nil, ErrInvalid
		}
		*destination, err = pinReconstructionDirectory(path)
		if err != nil || !directoryEmpty(path) {
			return nil, ErrInvalid
		}
	}
	if err = disableReconstructionTelemetry(workspace.home.path); err != nil {
		return nil, ErrInvalid
	}
	workspace.env = reconstructionEnvironment(baseEnv, workspace)
	if workspace.verify() != nil {
		return nil, ErrInvalid
	}
	keep = true
	return workspace, nil
}

// The Go telemetry library reads its mode file directly and does not use the
// GOTELEMETRY environment variable. Seed the private home with an explicit off
// mode before invoking Go so it cannot launch a sidecar that outlives cleanup.
func disableReconstructionTelemetry(home string) error {
	var config string
	switch runtime.GOOS {
	case "darwin":
		config = filepath.Join(home, "Library", "Application Support")
	case "linux":
		config = filepath.Join(home, ".config")
	default:
		return ErrInvalid
	}
	directory := filepath.Join(config, "go", "telemetry")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return ErrInvalid
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return ErrInvalid
	}
	if err := os.WriteFile(filepath.Join(directory, "mode"), []byte("off 2000-01-01\n"), 0600); err != nil {
		return ErrInvalid
	}
	return nil
}

func validGoReconstructionPolicy(policy goReconstructionPolicyOptions) bool {
	for _, value := range []string{policy.policy, policy.moduleProxy, policy.checksumDatabase} {
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

func pinReconstructionDirectory(path string) (pinnedReconstructionDirectory, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return pinnedReconstructionDirectory{}, ErrInvalid
	}
	return pinnedReconstructionDirectory{path: path, info: info}, nil
}

func (directory pinnedReconstructionDirectory) verify() error {
	if !filepath.IsAbs(directory.path) || directory.info == nil {
		return ErrInvalid
	}
	info, err := os.Lstat(directory.path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0700 || !os.SameFile(directory.info, info) {
		return ErrInvalid
	}
	return nil
}

func (workspace *goReconstruction) verify() error {
	if workspace == nil || workspace.anchor == nil || workspace.goExecutable.verify() != nil || workspace.root.verify() != nil ||
		workspace.moduleCache.verify() != nil || workspace.buildCache.verify() != nil ||
		workspace.goPath.verify() != nil || workspace.home.verify() != nil || workspace.temporary.verify() != nil {
		return ErrInvalid
	}
	info, err := workspace.anchor.Stat(".")
	if err != nil || !info.IsDir() || !os.SameFile(workspace.root.info, info) {
		return ErrInvalid
	}
	return nil
}

// goOutput runs the pinned Go executable while keeping diagnostic stderr out
// of structured stdout. Fresh-cache download notices are expected on stderr;
// merging them with go-list JSON would make the first reconstruction differ
// from later cache-warm invocations. Both streams remain bounded.
func (workspace *goReconstruction) goOutput(ctx context.Context, directory string, env []string, args ...string) (string, error) {
	if ctx == nil || directory == "" || workspace == nil || workspace.verify() != nil ||
		!validReconstructionEnvironment(env, workspace) {
		return "", ErrInvalid
	}
	commandContext, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(commandContext, workspace.goExecutable.path, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = directory
	cmd.Env = env
	var stdout, stderr boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil || stdout.overflow || stderr.overflow || workspace.verify() != nil {
		return "", ErrInvalid
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (workspace *goReconstruction) close() error {
	if workspace == nil || workspace.anchor == nil {
		return ErrInvalid
	}
	drifted := workspace.verify() != nil
	if err := workspace.cleanup(); err != nil || drifted {
		return ErrInvalid
	}
	return nil
}

// cleanup erases contents through the open root capability even when a child
// path has been replaced since it was pinned. Directory permissions are
// changed through already-open file handles, avoiding path-based chmod races.
// The final pathname is removed only when it still names the pinned root.
func (workspace *goReconstruction) cleanup() error {
	anchor := workspace.anchor
	workspace.anchor = nil
	if anchor == nil {
		return ErrInvalid
	}
	cleanupErr := cleanReconstructionDirectory(anchor)
	closeErr := anchor.Close()
	pathInfo, pathErr := os.Lstat(workspace.root.path)
	removeErr := error(nil)
	if pathErr == nil && os.SameFile(workspace.root.info, pathInfo) {
		removeErr = os.Remove(workspace.root.path)
	} else if pathErr != nil && !os.IsNotExist(pathErr) {
		removeErr = pathErr
	}
	if cleanupErr != nil || closeErr != nil || removeErr != nil {
		return ErrInvalid
	}
	if info, err := os.Lstat(workspace.root.path); err == nil && os.SameFile(workspace.root.info, info) {
		return ErrInvalid
	}
	return nil
}

func cleanReconstructionDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return ErrInvalid
	}
	if err = directory.Chmod(0700); err != nil {
		directory.Close()
		return ErrInvalid
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return ErrInvalid
	}
	for _, entry := range entries {
		name := entry.Name()
		info, statErr := root.Lstat(name)
		if statErr != nil {
			return ErrInvalid
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			child, openErr := root.OpenRoot(name)
			if openErr != nil {
				return ErrInvalid
			}
			childErr := cleanReconstructionDirectory(child)
			childCloseErr := child.Close()
			if childErr != nil || childCloseErr != nil {
				return ErrInvalid
			}
		}
		if err = root.Remove(name); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

func directoryEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

func reconstructionEnvironment(base []string, workspace *goReconstruction) []string {
	result := make([]string, 0, 32)
	systemRoot := ""
	for _, value := range base {
		key, _, found := strings.Cut(value, "=")
		if found && key == "SYSTEMROOT" && systemRoot == "" {
			systemRoot = value
		}
	}
	if systemRoot != "" {
		result = append(result, systemRoot)
	}
	return append(result,
		"PATH="+filepath.Dir(workspace.goExecutable.path),
		"LANG=C", "LC_ALL=C", "TZ=UTC",
		"HOME="+workspace.home.path,
		"TMPDIR="+workspace.temporary.path,
		"GOMODCACHE="+workspace.moduleCache.path,
		"GOCACHE="+workspace.buildCache.path,
		"GOPATH="+workspace.goPath.path,
		"GOPROXY="+workspace.policy.moduleProxy,
		"GOSUMDB="+workspace.policy.checksumDatabase,
		"GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOINSECURE=",
		"GOAUTH=off", "GOVCS=*:off", "GOTELEMETRY=off",
		"GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0",
		"GOENV=off", "GOAMD64=v1", "GOARM64=v8.0", "GOEXPERIMENT=", "GODEBUG=",
	)
}

func validReconstructionEnvironment(env []string, workspace *goReconstruction) bool {
	if workspace == nil {
		return false
	}
	expected := map[string]string{
		"PATH": filepath.Dir(workspace.goExecutable.path), "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
		"HOME": workspace.home.path, "TMPDIR": workspace.temporary.path,
		"GOMODCACHE": workspace.moduleCache.path, "GOCACHE": workspace.buildCache.path, "GOPATH": workspace.goPath.path,
		"GOPROXY": workspace.policy.moduleProxy, "GOSUMDB": workspace.policy.checksumDatabase,
		"GOPRIVATE": "", "GONOPROXY": "", "GONOSUMDB": "", "GOINSECURE": "",
		"GOAUTH": "off", "GOVCS": "*:off", "GOTELEMETRY": "off", "GOFLAGS": "",
		"GOWORK": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOENV": "off",
		"GOAMD64": "v1", "GOARM64": "v8.0", "GOEXPERIMENT": "", "GODEBUG": "",
	}
	seen := make(map[string]bool, len(env))
	for _, value := range env {
		key, actual, found := strings.Cut(value, "=")
		if !found || seen[key] {
			return false
		}
		seen[key] = true
		if wanted, ok := expected[key]; ok {
			if actual != wanted {
				return false
			}
			continue
		}
		if key == "GOOS" {
			if actual != "darwin" && actual != "linux" {
				return false
			}
			continue
		}
		if key == "GOARCH" {
			if actual != "amd64" && actual != "arm64" {
				return false
			}
			continue
		}
		if key != "SYSTEMROOT" || actual == "" {
			return false
		}
	}
	for key := range expected {
		if !seen[key] {
			return false
		}
	}
	return seen["GOOS"] == seen["GOARCH"]
}

// canonicalProspectivePath resolves an existing path or its nearest existing
// parent without allowing a missing suffix to escape through dot components.
func canonicalProspectivePath(path string) (string, error) {
	if path == "" {
		return "", ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", ErrInvalid
	}
	current := filepath.Clean(absolute)
	var suffix []string
	for {
		_, err = os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil || !filepath.IsAbs(resolved) {
				return "", ErrInvalid
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", ErrInvalid
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", ErrInvalid
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func reconstructionPathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
