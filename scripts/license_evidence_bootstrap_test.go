package scripts

import (
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
	fake := `#!/bin/sh
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
	for key, expected := range map[string]string{
		"GOENV": "off", "GOFLAGS": "", "GOWORK": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0",
		"GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GOPRIVATE": "",
		"GONOPROXY": "", "GONOSUMDB": "", "GOINSECURE": "", "GOAUTH": "off", "GOVCS": "*:off", "GOTELEMETRY": "off",
		"HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "NO_PROXY": "",
		"ARGS": "<run><./cmd/license-evidence><freeze><--commit><" + strings.Repeat("a", 40) + "><--source><" + repository + "><--out></evidence.json>",
	} {
		if values[key] != expected {
			t.Fatalf("%s=%q, want %q", key, values[key], expected)
		}
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

func TestLicenseEvidenceBootstrapRemovesWorkspaceAfterCommandFailure(t *testing.T) {
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
		t.Fatal("failing license-evidence command unexpectedly succeeded")
	}
	if string(output) != "license-evidence bootstrap failed\n" || strings.Contains(string(output), secret) || strings.Contains(string(output), bootstrapParent) {
		t.Fatalf("command failure leaked subprocess data: %q", output)
	}
	entries, err := os.ReadDir(bootstrapParent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed bootstrap workspace was retained: %v, entries=%v", err, entries)
	}
}

func TestLicenseEvidenceBootstrapRejectsSuccessfulStderrWithoutLeak(t *testing.T) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeGo := filepath.Join(bin, "go")
	secret := "unexpected-stderr-secret"
	fake := "#!/bin/sh\nprintf '%s\\n' 'sha256:" + strings.Repeat("b", 64) + "'\nprintf '%s\\n' '" + secret + "' >&2\n"
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
	if runErr == nil || string(output) != "license-evidence bootstrap failed\n" || strings.Contains(string(output), secret) {
		t.Fatalf("unexpected stderr was not rejected safely: %v: %q", runErr, output)
	}
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
			if writeErr := os.WriteFile(fakeGo, []byte("#!/bin/sh\nprintf '%s\\n' 'sha256:"+strings.Repeat("c", 64)+"'\n"), 0700); writeErr != nil {
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
