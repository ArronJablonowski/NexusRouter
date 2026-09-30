package releasepack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/sumdb/note"
)

const reconstructionFixtureVersion = "v1.0.0"

type reconstructionFixtureModule struct {
	path, version, modSum, zipSum string
	mod, archive                  []byte
}

type reconstructionFixture struct {
	t       *testing.T
	server  *httptest.Server
	policy  goReconstructionPolicyOptions
	modules map[string]reconstructionFixtureModule

	mu       sync.Mutex
	requests map[string]int
	fault    func(string, reconstructionFixtureModule) (int, []byte, bool)
}

func newReconstructionFixture(t *testing.T) *reconstructionFixture {
	t.Helper()
	fixture := &reconstructionFixture{t: t, modules: make(map[string]reconstructionFixtureModule), requests: make(map[string]int)}
	fixture.addModule("example.com/darwin-fixture/transitive", map[string]string{
		"go.mod":     "module example.com/darwin-fixture/transitive\n\ngo 1.27.1\n",
		"fixture.go": "package transitive\n\nconst Value = true\n",
		"LICENSE":    "MIT License\n\nfixture transitive\n",
	})
	fixture.addModule("example.com/darwin-fixture/common", map[string]string{
		"go.mod":     "module example.com/darwin-fixture/common\n\ngo 1.27.1\n\nrequire example.com/darwin-fixture/transitive v1.0.0\n",
		"fixture.go": "package common\n\nimport _ \"example.com/darwin-fixture/transitive\"\n",
		"LICENSE":    "MIT License\n\nfixture common\n",
	})
	for _, dimension := range []string{"os-darwin", "os-linux", "arch-amd64", "arch-arm64"} {
		path := "example.com/darwin-fixture/" + dimension
		fixture.addModule(path, map[string]string{
			"go.mod":     "module " + path + "\n\ngo 1.27.1\n",
			"fixture.go": "package platform\n\nconst Value = true\n",
			"LICENSE":    "MIT License\n\nfixture " + dimension + "\n",
		})
	}
	signer, verifier, err := note.GenerateKey(rand.Reader, "sum.test")
	if err != nil {
		t.Fatal(err)
	}
	operations := sumdb.NewTestServer(signer, func(path, version string) ([]byte, error) {
		item, ok := fixture.modules[path+"@"+version]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", path, version, item.zipSum, path, version, item.modSum)), nil
	})
	mux := http.NewServeMux()
	sumHandler := http.StripPrefix("/sumdb", sumdb.NewServer(operations))
	mux.Handle("/sumdb/", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		fixture.requests[request.URL.Path]++
		fixture.mu.Unlock()
		sumHandler.ServeHTTP(writer, request)
	}))
	mux.HandleFunc("/proxy/", fixture.serveProxy)
	fixture.server = httptest.NewServer(mux)
	t.Cleanup(fixture.server.Close)
	fixture.policy = goReconstructionPolicyOptions{
		policy:           "nexusrouter-controlled-reconstruction-v1",
		moduleProxy:      fixture.server.URL + "/proxy",
		checksumDatabase: verifier + " " + fixture.server.URL + "/sumdb",
	}
	return fixture
}

func (fixture *reconstructionFixture) addModule(modulePath string, files map[string]string) {
	fixture.t.Helper()
	version := reconstructionFixtureVersion
	prefix := modulePath + "@" + version + "/"
	names := make([]string, 0, len(files))
	content := make(map[string][]byte, len(files))
	for name, body := range files {
		names = append(names, prefix+name)
		content[prefix+name] = []byte(body)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetModTime(time.Unix(0, 0).UTC())
		file, err := writer.CreateHeader(header)
		if err != nil {
			fixture.t.Fatal(err)
		}
		if _, err = file.Write(content[name]); err != nil {
			fixture.t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		fixture.t.Fatal(err)
	}
	zipSum, err := dirhash.Hash1(names, func(name string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content[name])), nil
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	modBody := []byte(files["go.mod"])
	modSum, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(modBody)), nil
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	fixture.modules[modulePath+"@"+version] = reconstructionFixtureModule{
		path: modulePath, version: version, modSum: modSum, zipSum: zipSum, mod: modBody, archive: archive.Bytes(),
	}
}

func (fixture *reconstructionFixture) serveProxy(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	fixture.requests[request.URL.Path]++
	fixture.mu.Unlock()
	relative := strings.TrimPrefix(request.URL.Path, "/proxy/")
	modulePart, filePart, ok := strings.Cut(relative, "/@v/")
	if !ok {
		http.NotFound(writer, request)
		return
	}
	modulePath, err := module.UnescapePath(modulePart)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	if filePart == "list" {
		_, _ = io.WriteString(writer, reconstructionFixtureVersion+"\n")
		return
	}
	version, suffix := "", ""
	for _, candidate := range []string{"mod", "zip", "info"} {
		ending := "." + candidate
		if strings.HasSuffix(filePart, ending) {
			version, suffix = strings.TrimSuffix(filePart, ending), candidate
			break
		}
	}
	item, ok := fixture.modules[modulePath+"@"+version]
	if !ok {
		http.NotFound(writer, request)
		return
	}
	fixture.mu.Lock()
	fault := fixture.fault
	fixture.mu.Unlock()
	if fault != nil {
		if status, body, handled := fault(request.URL.Path, item); handled {
			writer.WriteHeader(status)
			_, _ = writer.Write(body)
			return
		}
	}
	switch suffix {
	case "mod":
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write(item.mod)
	case "zip":
		writer.Header().Set("Content-Type", "application/zip")
		_, _ = writer.Write(item.archive)
	case "info":
		_ = json.NewEncoder(writer).Encode(map[string]string{"Version": version, "Time": "2020-01-01T00:00:00Z"})
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *reconstructionFixture) sourceRepository() (string, string) {
	fixture.t.Helper()
	source := filepath.Join(fixture.t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(source, "cmd", "nexus"), 0755); err != nil {
		fixture.t.Fatal(err)
	}
	paths := make([]string, 0, len(fixture.modules))
	for _, item := range fixture.modules {
		paths = append(paths, item.path)
	}
	sort.Strings(paths)
	var goMod strings.Builder
	goMod.WriteString("module example.com/darwin-fixture/root\n\ngo 1.27.1\n\nrequire (\n")
	for _, path := range paths {
		fmt.Fprintf(&goMod, "\t%s %s\n", path, reconstructionFixtureVersion)
	}
	goMod.WriteString(")\n")
	var goSum strings.Builder
	for _, path := range paths {
		item := fixture.modules[path+"@"+reconstructionFixtureVersion]
		fmt.Fprintf(&goSum, "%s %s %s\n%s %s/go.mod %s\n", path, item.version, item.zipSum, path, item.version, item.modSum)
	}
	files := map[string]string{
		"go.mod": goMod.String(), "go.sum": goSum.String(),
		"LICENSE":                                "MIT License\n\nCopyright controlled reconstruction fixture\n",
		filepath.Join("cmd", "nexus", "main.go"): "package main\n\nimport _ \"example.com/darwin-fixture/common\"\n\nfunc main() {}\n",
	}
	for _, osName := range []string{"darwin", "linux"} {
		modulePath := "example.com/darwin-fixture/os-" + osName
		files[filepath.Join("cmd", "nexus", "platform_"+osName+".go")] = "//go:build " + osName + "\n\npackage main\n\nimport _ \"" + modulePath + "\"\n"
	}
	for _, arch := range []string{"amd64", "arm64"} {
		modulePath := "example.com/darwin-fixture/arch-" + arch
		files[filepath.Join("cmd", "nexus", "platform_"+arch+".go")] = "//go:build " + arch + "\n\npackage main\n\nimport _ \"" + modulePath + "\"\n"
	}
	for name, body := range files {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			fixture.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			fixture.t.Fatal(err)
		}
	}
	env := append(environment(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	for _, args := range [][]string{{"git", "init", "--quiet"}, {"git", "add", "."}, {"git", "commit", "--quiet", "-m", "fixture"}} {
		if _, err := command(fixture.t.Context(), source, env, args[0], args[1:]...); err != nil {
			fixture.t.Fatal(err)
		}
	}
	commit, err := command(fixture.t.Context(), source, environment(), "git", "rev-parse", "HEAD")
	if err != nil {
		fixture.t.Fatal(err)
	}
	return source, commit
}

func (fixture *reconstructionFixture) zipRequestCount() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	total := 0
	for path, count := range fixture.requests {
		if strings.HasSuffix(path, ".zip") {
			total += count
		}
	}
	return total
}

func (fixture *reconstructionFixture) sumDBRequestCount() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	total := 0
	for path, count := range fixture.requests {
		if strings.HasPrefix(path, "/sumdb/") {
			total += count
		}
	}
	return total
}

func (fixture *reconstructionFixture) probeSignedSumDB(policy goReconstructionPolicyOptions) (resultErr error) {
	fixture.t.Helper()
	source := fixture.t.TempDir()
	body := "module example.com/darwin-fixture/probe\n\ngo 1.27.1\n\nrequire example.com/darwin-fixture/common v1.0.0\n"
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte(body), 0644); err != nil {
		fixture.t.Fatal(err)
	}
	workspace, err := newGoReconstructionWithPolicy(environment(), policy, source)
	if err != nil {
		return err
	}
	root := workspace.root.path
	defer func() {
		if closeErr := workspace.close(); closeErr != nil {
			fixture.t.Errorf("checksum probe cleanup: %v", closeErr)
			if resultErr == nil {
				resultErr = closeErr
			}
		}
		if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
			fixture.t.Errorf("checksum probe retained its private cache: %v", statErr)
		}
	}()
	_, err = workspace.goOutput(fixture.t.Context(), source, workspace.env, "mod", "download", "all")
	return err
}

func TestControlledReconstructionFreezeAndVerifyUseIndependentCaches(t *testing.T) {
	fixture := newReconstructionFixture(t)
	source, commit := fixture.sourceRepository()
	reconstructionParent := t.TempDir()
	t.Setenv("TMPDIR", reconstructionParent)
	ambient := t.TempDir()
	marker := filepath.Join(ambient, "must-not-change")
	if err := os.WriteFile(marker, []byte("ambient\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMODCACHE", ambient)
	t.Setenv("GOCACHE", ambient)
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GH_TOKEN", "must-not-escape")
	out := filepath.Join(t.TempDir(), "evidence.json")
	digest, err := freezeLicenseEvidenceWithPolicy(t.Context(), LicenseEvidenceOptions{Commit: commit, Source: source, Out: out}, fixture.policy)
	if err != nil {
		t.Fatal("freeze", err)
	}
	freezeRequests := fixture.zipRequestCount()
	if freezeRequests == 0 {
		t.Fatal("controlled proxy was not used")
	}
	if err = verifyLicenseEvidenceRecordOnly(t.Context(), out, digest, source, fixture.policy); err != nil {
		t.Fatal("verify", err)
	}
	if fixture.zipRequestCount() <= freezeRequests {
		t.Fatal("verification reused the freeze module cache")
	}
	entries, err := os.ReadDir(ambient)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(marker) {
		t.Fatal("ambient cache or environment was consulted", err)
	}
	body, record, err := readLicenseEvidenceWithPolicy(out, fixture.policy)
	if err != nil || record.SchemaVersion != 3 || licenseEvidenceDigest(body) != digest {
		t.Fatal("schema-v3 evidence binding", err)
	}
	if validateLicenseEvidence(record) == nil {
		t.Fatal("production validator accepted controlled test endpoints")
	}
	graphs := make(map[string]bool)
	for _, target := range record.Targets {
		want := []string{
			"example.com/darwin-fixture/arch-" + target.Arch,
			"example.com/darwin-fixture/common",
			"example.com/darwin-fixture/os-" + target.OS,
			"example.com/darwin-fixture/transitive",
			goToolchainModulePath,
		}
		got := make([]string, 0, len(target.Modules))
		for _, item := range target.Modules {
			got = append(got, item.Path)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("wrong %s/%s module closure: got %v want %v", target.OS, target.Arch, got, want)
		}
		graphs[target.DependencyGraphSHA256] = true
	}
	if len(graphs) != len(licenseEvidenceTargets) {
		t.Fatal("target-specific graph closures were not distinct")
	}
	if !directoryEmpty(reconstructionParent) {
		t.Fatal("freeze or verify retained an isolated reconstruction cache")
	}
}

func TestControlledReconstructionUsesLocallySignedChecksumDatabase(t *testing.T) {
	fixture := newReconstructionFixture(t)
	reconstructionParent := t.TempDir()
	t.Setenv("TMPDIR", reconstructionParent)
	if err := fixture.probeSignedSumDB(fixture.policy); err != nil {
		t.Fatal("locally signed checksum database rejected", err)
	}
	if fixture.sumDBRequestCount() == 0 {
		t.Fatal("checksum database was not consulted")
	}
	_, wrongVerifier, err := note.GenerateKey(rand.Reader, "sum.test")
	if err != nil {
		t.Fatal(err)
	}
	badPolicy := fixture.policy
	badPolicy.checksumDatabase = wrongVerifier + " " + fixture.server.URL + "/sumdb"
	if err = fixture.probeSignedSumDB(badPolicy); err == nil {
		t.Fatal("wrong checksum-database signature accepted")
	}
	if !directoryEmpty(reconstructionParent) {
		t.Fatal("checksum failure retained an isolated reconstruction cache")
	}
}

func TestControlledReconstructionProxyFailuresDoNotPublishEvidence(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		fault func(string, reconstructionFixtureModule) (int, []byte, bool)
	}{
		{name: "server_error", fault: func(path string, _ reconstructionFixtureModule) (int, []byte, bool) {
			return http.StatusInternalServerError, []byte("unavailable\n"), strings.HasSuffix(path, ".zip")
		}},
		{name: "missing_module", fault: func(path string, _ reconstructionFixtureModule) (int, []byte, bool) {
			return http.StatusNotFound, []byte("missing\n"), strings.Contains(path, "os-darwin") && strings.HasSuffix(path, ".zip")
		}},
		{name: "truncated_zip", fault: func(path string, item reconstructionFixtureModule) (int, []byte, bool) {
			if !strings.Contains(path, "arch-amd64") || !strings.HasSuffix(path, ".zip") {
				return 0, nil, false
			}
			return http.StatusOK, item.archive[:len(item.archive)/2], true
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newReconstructionFixture(t)
			reconstructionParent := t.TempDir()
			t.Setenv("TMPDIR", reconstructionParent)
			fixture.mu.Lock()
			fixture.fault = scenario.fault
			fixture.mu.Unlock()
			source, commit := fixture.sourceRepository()
			out := filepath.Join(t.TempDir(), "evidence.json")
			if _, err := freezeLicenseEvidenceWithPolicy(t.Context(), LicenseEvidenceOptions{Commit: commit, Source: source, Out: out}, fixture.policy); err == nil {
				t.Fatal("faulted proxy reconstruction succeeded")
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("failed reconstruction published evidence", err)
			}
			if !directoryEmpty(reconstructionParent) {
				t.Fatal("failed reconstruction retained an isolated cache")
			}
		})
	}
}

func TestControlledReconstructionRejectsPolicyAndSourceDrift(t *testing.T) {
	fixture := newReconstructionFixture(t)
	source, commit := fixture.sourceRepository()
	reconstructionParent := t.TempDir()
	t.Setenv("TMPDIR", reconstructionParent)
	out := filepath.Join(t.TempDir(), "evidence.json")
	digest, err := freezeLicenseEvidenceWithPolicy(t.Context(), LicenseEvidenceOptions{Commit: commit, Source: source, Out: out}, fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	before := fixture.zipRequestCount()
	driftedPolicy := fixture.policy
	driftedPolicy.policy += "-drift"
	if err = verifyLicenseEvidenceRecordOnly(t.Context(), out, digest, source, driftedPolicy); err == nil {
		t.Fatal("reconstruction policy drift accepted")
	}
	if fixture.zipRequestCount() != before {
		t.Fatal("policy drift was not rejected before reconstruction")
	}
	mainPath := filepath.Join(source, "cmd", "nexus", "main.go")
	mainBody, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(mainPath, append(mainBody, []byte("\n// drift\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	if err = verifyLicenseEvidenceRecordOnly(t.Context(), out, digest, source, fixture.policy); err == nil {
		t.Fatal("source mutation accepted")
	}
	if !directoryEmpty(reconstructionParent) {
		t.Fatal("policy or source drift retained an isolated cache")
	}
}

func verifyLicenseEvidenceRecordOnly(ctx context.Context, recordPath, digest, source string, policy goReconstructionPolicyOptions) error {
	_, err := verifyLicenseEvidenceRecordWithPolicy(ctx, recordPath, digest, source, policy)
	return err
}
