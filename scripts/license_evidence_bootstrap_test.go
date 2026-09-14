package scripts

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLicenseEvidenceBootstrapUsesFreshFixedEnvironment(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh")
	bin := t.TempDir()
	fakeGo := filepath.Join(bin, "go")
	capture := filepath.Join(t.TempDir(), "environment")
	buildCapture := capture + ".build"
	verifier := `#!/bin/sh
capture=` + strconv.Quote(capture) + `
{
for name in HOME TMPDIR GOPATH GOMODCACHE GOCACHE GOENV GOFLAGS GOWORK GOTOOLCHAIN CGO_ENABLED GOPROXY GOSUMDB GOPRIVATE GONOPROXY GONOSUMDB GOINSECURE GOAUTH GOVCS GOTELEMETRY HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY; do
  eval "value=\${$name-}"
  printf '%s=%s\n' "$name" "$value"
done
for directory in "$HOME" "$TMPDIR" "$GOPATH" "$GOMODCACHE" "$GOCACHE"; do
  test -z "$(find "$directory" ! -path "$directory" -print -quit)" || exit 23
done
printf 'ARGS='
for argument in "$@"; do printf '<%s>' "$argument"; done
printf '\n'
} >"$capture"
printf '%s\n' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
`
	fake := fakeGoBuilder(verifier, `capture=`+strconv.Quote(buildCapture)+`
{
for name in HOME TMPDIR GOPATH GOMODCACHE GOCACHE GOENV GOFLAGS GOWORK GOTOOLCHAIN CGO_ENABLED GOPROXY GOSUMDB GOPRIVATE GONOPROXY GONOSUMDB GOINSECURE GOAUTH GOVCS GOTELEMETRY HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY; do
  eval "value=\${$name-}"
  printf '%s=%s\n' "$name" "$value"
done
printf 'ARGS='
for argument in "$@"; do printf '<%s>' "$argument"; done
printf '\n'
} >"$capture"
printf '%s\n' 'ordinary build download diagnostic' >&2`, 0)
	if err = os.WriteFile(fakeGo, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapParent := t.TempDir()
	bootstrapParentReal, err := filepath.EvalSymlinks(bootstrapParent)
	if err != nil {
		t.Fatal(err)
	}
	ambient := filepath.Join(t.TempDir(), "ambient-cache")
	if err = os.Mkdir(ambient, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", script, "freeze", "--commit", strings.Repeat("a", 40), "--source", repository, "--out", "/evidence.json")
	command.Dir = repository
	command.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"DARWIN_LICENSE_BOOTSTRAP_PARENT=" + bootstrapParent,
		"HOME=/ambient/home", "TMPDIR=/ambient/tmp", "GOPATH=/ambient/gopath",
		"GOMODCACHE=" + ambient, "GOCACHE=/ambient/build", "GOENV=/ambient/goenv",
		"GOFLAGS=-mod=vendor", "GOWORK=/ambient/go.work", "GOTOOLCHAIN=auto", "CGO_ENABLED=1",
		"GOPROXY=https://proxy.invalid,direct", "GOSUMDB=off", "GOPRIVATE=private.invalid",
		"GONOPROXY=private.invalid", "GONOSUMDB=private.invalid", "GOINSECURE=private.invalid",
		"GOAUTH=netrc", "GOVCS=*:all", "GOTELEMETRY=on", "HTTPS_PROXY=https://credential.invalid",
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap failed: %v: %s", err, output)
	}
	if string(output) != "sha256:"+strings.Repeat("a", 64)+"\n" {
		t.Fatalf("unexpected successful output %q", output)
	}
	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	values := parseBootstrapOutput(t, string(captured))
	buildBody, err := os.ReadFile(buildCapture)
	if err != nil {
		t.Fatal(err)
	}
	buildValues := parseBootstrapOutput(t, string(buildBody))
	for key, expected := range map[string]string{
		"GOENV": "off", "GOFLAGS": "", "GOWORK": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0",
		"GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GOPRIVATE": "",
		"GONOPROXY": "", "GONOSUMDB": "", "GOINSECURE": "", "GOAUTH": "off", "GOVCS": "*:off", "GOTELEMETRY": "off",
		"HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "NO_PROXY": "",
		"ARGS": "<freeze><--commit><" + strings.Repeat("a", 40) + "><--source><" + repository + "><--out></evidence.json>",
	} {
		if values[key] != expected {
			t.Fatalf("%s=%q, want %q", key, values[key], expected)
		}
		if key != "ARGS" && buildValues[key] != expected {
			t.Fatalf("build %s=%q, want %q", key, buildValues[key], expected)
		}
	}
	if args := buildValues["ARGS"]; !strings.HasPrefix(args, "<build><-o><"+bootstrapParentReal+string(filepath.Separator)) || !strings.HasSuffix(args, "><./cmd/license-evidence>") {
		t.Fatalf("unexpected private build arguments %q", args)
	}
	for _, key := range []string{"HOME", "TMPDIR", "GOPATH", "GOMODCACHE", "GOCACHE"} {
		value := values[key]
		if value == "" || strings.Contains(value, "/ambient/") || !strings.HasPrefix(value, bootstrapParentReal+string(filepath.Separator)) {
			t.Fatalf("%s was not isolated: %q", key, value)
		}
		root := filepath.Dir(value)
		if key == "HOME" {
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("bootstrap root was not removed: %q: %v", root, statErr)
			}
		}
	}
}

func TestLicenseEvidenceBootstrapRejectsUnsafeParentBeforeGo(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"), "verify")
	command.Dir = repository
	command.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_LICENSE_BOOTSTRAP_PARENT=relative"}
	if output, runErr := command.CombinedOutput(); runErr == nil || string(output) != "license-evidence bootstrap failed\n" {
		t.Fatalf("unsafe bootstrap parent accepted: %v: %q", runErr, output)
	}
}

func TestLicenseEvidenceBootstrapRemovesWorkspaceAfterBuildFailure(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeGo := filepath.Join(bin, "go")
	secret := "ambient-token-must-not-escape"
	fake := "#!/bin/sh\nprintf '%s\\n' \"$HOME/private-cache-path\"\nprintf '%s\\n' '" + secret + "' >&2\nexit 29\n"
	if err = os.WriteFile(fakeGo, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapParent := t.TempDir()
	command := exec.Command("sh", filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"), "freeze")
	command.Dir = repository
	command.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"DARWIN_LICENSE_BOOTSTRAP_PARENT=" + bootstrapParent,
	}
	output, runErr := command.CombinedOutput()
	if runErr == nil {
		t.Fatal("failing license-evidence build unexpectedly succeeded")
	}
	if string(output) != "license-evidence bootstrap failed\n" || strings.Contains(string(output), secret) || strings.Contains(string(output), bootstrapParent) {
		t.Fatalf("command failure leaked subprocess data: %q", output)
	}
	entries, err := os.ReadDir(bootstrapParent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed bootstrap workspace was retained: %v, entries=%v", err, entries)
	}
}

func TestLicenseEvidenceBootstrapRejectsSuccessfulVerifierStderr(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeGo := filepath.Join(bin, "go")
	secret := "unexpected-stderr-secret"
	verifier := "#!/bin/sh\nprintf '%s\\n' 'sha256:" + strings.Repeat("b", 64) + "'\nprintf '%s\\n' '" + secret + "' >&2\n"
	fake := fakeGoBuilder(verifier, "printf '%s\\n' 'ordinary build download diagnostic' >&2", 0)
	if err = os.WriteFile(fakeGo, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapParent := t.TempDir()
	command := exec.Command("sh", filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"), "freeze")
	command.Dir = repository
	command.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"DARWIN_LICENSE_BOOTSTRAP_PARENT=" + bootstrapParent,
	}
	output, runErr := command.CombinedOutput()
	if runErr == nil || string(output) != "license-evidence bootstrap failed\n" || strings.Contains(string(output), secret) || strings.Contains(string(output), "ordinary build") {
		t.Fatalf("successful verifier stderr was not rejected safely: %v: %q", runErr, output)
	}
	if entries, readErr := os.ReadDir(bootstrapParent); readErr != nil || len(entries) != 0 {
		t.Fatalf("stderr failure retained bootstrap workspace: %v, entries=%v", readErr, entries)
	}
}

func TestLicenseEvidenceBootstrapRealGoUsesEmptyLocalProxyCache(t *testing.T) {
	repository := t.TempDir()
	scriptBody, err := os.ReadFile("license-evidence-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	proxy := filepath.Join(t.TempDir(), "proxy")
	writeProxyModule(t, proxy, "example.com/bootstrapdep", "v1.0.0", map[string]string{
		"go.mod":       "module example.com/bootstrapdep\n\ngo 1.27\n",
		"bootstrap.go": "package bootstrapdep\n\nconst Value = \"downloaded\"\n",
	})
	patched := bytes.ReplaceAll(scriptBody, []byte("GOPROXY=https://proxy.golang.org"), []byte("GOPROXY=file://"+proxy))
	patched = bytes.ReplaceAll(patched, []byte("GOSUMDB=sum.golang.org"), []byte("GOSUMDB=off"))
	if bytes.Equal(patched, scriptBody) {
		t.Fatal("bootstrap policy literals were not replaced for controlled fixture")
	}
	if err = os.MkdirAll(filepath.Join(repository, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"), patched, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(repository, "cmd", "license-evidence"), 0700); err != nil {
		t.Fatal(err)
	}
	goMod := "module example.com/bootstraptest\n\ngo 1.27\n\nrequire example.com/bootstrapdep v1.0.0\n"
	if err = os.WriteFile(filepath.Join(repository, "go.mod"), []byte(goMod), 0600); err != nil {
		t.Fatal(err)
	}
	mainSource := `package main

import (
	"crypto/sha256"
	"fmt"
	"os"

	"example.com/bootstrapdep"
)

func main() {
	if len(os.Args) != 6 || os.Args[1] != "freeze" || os.Args[4] != "--out" {
		os.Exit(2)
	}
	file, err := os.OpenFile(os.Args[5], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(1)
	}
	if _, err = file.WriteString(bootstrapdep.Value); err != nil || file.Sync() != nil || file.Close() != nil {
		os.Exit(1)
	}
	sum := sha256.Sum256([]byte(bootstrapdep.Value))
	fmt.Printf("sha256:%x\n", sum)
}
`
	if err = os.WriteFile(filepath.Join(repository, "cmd", "license-evidence", "main.go"), []byte(mainSource), 0600); err != nil {
		t.Fatal(err)
	}
	seedModuleCache := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(seedModuleCache, func(path string, _ os.FileInfo, _ error) error {
			_ = os.Chmod(path, 0700)
			return nil
		})
	})
	seedSums := exec.Command("go", "mod", "tidy")
	seedSums.Dir = repository
	seedSums.Env = append(os.Environ(),
		"GOPROXY=file://"+proxy, "GOSUMDB=off", "GOMODCACHE="+seedModuleCache, "GOCACHE="+t.TempDir())
	if output, downloadErr := seedSums.CombinedOutput(); downloadErr != nil {
		t.Fatalf("seed fixture go.sum: %v: %s", downloadErr, output)
	}
	outputPath := filepath.Join(t.TempDir(), "evidence.json")
	bootstrapParent := t.TempDir()
	command := exec.Command("sh", filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"),
		"freeze", "--commit", strings.Repeat("a", 40), "--out", outputPath)
	command.Dir = repository
	command.Env = append(os.Environ(), "DARWIN_LICENSE_BOOTSTRAP_PARENT="+bootstrapParent)
	output, runErr := command.CombinedOutput()
	if runErr != nil {
		t.Fatalf("real empty-cache bootstrap failed: %v: %q", runErr, output)
	}
	expected := sha256Line("downloaded")
	if string(output) != expected {
		t.Fatalf("unexpected bootstrap output %q, want %q", output, expected)
	}
	if body, readErr := os.ReadFile(outputPath); readErr != nil || string(body) != "downloaded" {
		t.Fatalf("unexpected evidence residue: %q, %v", body, readErr)
	}
	if entries, readErr := os.ReadDir(bootstrapParent); readErr != nil || len(entries) != 0 {
		t.Fatalf("bootstrap workspace retained: %v, entries=%v", readErr, entries)
	}
}

func TestLicenseEvidenceBootstrapNonzeroVerifierPreservesOutputResidueButLeaksNothing(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	residue := filepath.Join(t.TempDir(), "uncertain-evidence.json")
	secret := "nonzero-go-secret"
	verifier := "#!/bin/sh\nprintf '%s' residue >" + strconv.Quote(residue) + "\nprintf '%s\\n' digest-like-output\nprintf '%s\\n' " + strconv.Quote(secret) + " >&2\nexit 41\n"
	fake := fakeGoBuilder(verifier, "", 0)
	if err = os.WriteFile(filepath.Join(bin, "go"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapParent := t.TempDir()
	command := exec.Command("sh", filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"), "freeze")
	command.Dir = repository
	command.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "DARWIN_LICENSE_BOOTSTRAP_PARENT=" + bootstrapParent}
	output, runErr := command.CombinedOutput()
	if runErr == nil || string(output) != "license-evidence bootstrap failed\n" || strings.Contains(string(output), secret) || strings.Contains(string(output), "digest-like-output") {
		t.Fatalf("nonzero verifier failure was not generic: %v: %q", runErr, output)
	}
	if body, readErr := os.ReadFile(residue); readErr != nil || string(body) != "residue" {
		t.Fatalf("external residue was altered: %q, %v", body, readErr)
	}
	if entries, readErr := os.ReadDir(bootstrapParent); readErr != nil || len(entries) != 0 {
		t.Fatalf("failed bootstrap workspace retained: %v, entries=%v", readErr, entries)
	}
}

func writeProxyModule(t *testing.T, proxy, module, version string, files map[string]string) {
	t.Helper()
	directory := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	mod := files["go.mod"]
	if err := os.WriteFile(filepath.Join(directory, version+".mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, version+".info"), []byte(`{"Version":"`+version+`","Time":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(filepath.Join(directory, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	for name, body := range files {
		entry, createErr := zipWriter.Create(module + "@" + version + "/" + name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte(body)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err = zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
}

func sha256Line(value string) string {
	// Fixed independently for the fixture payload; keeping the helper tiny makes
	// the stdout assertion readable without coupling it to the generated file.
	if value != "downloaded" {
		panic("unexpected fixture value")
	}
	return "sha256:b7a8a844a613be796bc1892dc480f9d92c50d32a5713a87758e5c5addc4ec814\n"
}

func fakeGoBuilder(verifier, buildActions string, status int) string {
	script := "#!/bin/sh\n" + buildActions + "\n"
	if status != 0 {
		return script + "exit " + strconv.Itoa(status) + "\n"
	}
	return script + `
test "${1-}" = build || exit 91
shift
output=
while test "$#" -gt 0; do
  if test "$1" = -o; then
    shift
    test "$#" -gt 0 || exit 92
    output=$1
  fi
  shift
done
test -n "$output" || exit 93
cat >"$output" <<'DARWIN_LICENSE_EVIDENCE_FIXTURE'
` + verifier + `
DARWIN_LICENSE_EVIDENCE_FIXTURE
chmod 0700 "$output" || exit 94
`
}

func TestLicenseEvidenceBootstrapRedactsUtilityFailures(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, utility := range []string{"mktemp", "mkdir", "find", "chmod", "wc", "grep", "rm"} {
		t.Run(utility, func(t *testing.T) {
			bin := t.TempDir()
			fakeGo := filepath.Join(bin, "go")
			verifier := "#!/bin/sh\nprintf '%s\\n' 'sha256:" + strings.Repeat("c", 64) + "'\n"
			if writeErr := os.WriteFile(fakeGo, []byte(fakeGoBuilder(verifier, "", 0)), 0700); writeErr != nil {
				t.Fatal(writeErr)
			}
			secret := "utility-secret-must-not-escape"
			fakeUtility := "#!/bin/sh\nprintf '%s\\n' '" + secret + "' \"$@\" >&2\nexit 31\n"
			if writeErr := os.WriteFile(filepath.Join(bin, utility), []byte(fakeUtility), 0700); writeErr != nil {
				t.Fatal(writeErr)
			}
			bootstrapParent := t.TempDir()
			command := exec.Command("sh", filepath.Join(repository, "scripts", "license-evidence-bootstrap.sh"), "freeze")
			command.Dir = repository
			command.Env = []string{
				"PATH=" + bin + ":/usr/bin:/bin",
				"DARWIN_LICENSE_BOOTSTRAP_PARENT=" + bootstrapParent,
			}
			output, runErr := command.CombinedOutput()
			if runErr == nil || string(output) != "license-evidence bootstrap failed\n" ||
				strings.Contains(string(output), secret) || strings.Contains(string(output), bootstrapParent) {
				t.Fatalf("%s failure was not redacted: %v: %q", utility, runErr, output)
			}
			// An injected rm failure necessarily prevents the script from proving
			// cleanup. The test owns the parent and removes any retained fixture.
			entries, readErr := os.ReadDir(bootstrapParent)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if utility != "rm" && len(entries) != 0 {
				t.Fatalf("%s failure retained workspace entries: %v", utility, entries)
			}
			for _, entry := range entries {
				if removeErr := os.RemoveAll(filepath.Join(bootstrapParent, entry.Name())); removeErr != nil {
					t.Fatal(removeErr)
				}
			}
		})
	}
}

func parseBootstrapOutput(t *testing.T, output string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			t.Fatalf("malformed bootstrap output %q", line)
		}
		result[key] = value
	}
	return result
}
