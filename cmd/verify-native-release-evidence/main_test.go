package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

func TestRunVerifiesCanonicalEvidenceBundle(t *testing.T) {
	version, commit := "1.0.0-rc.11", strings.Repeat("a", 40)
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	install := releasepack.InstallRehearsalEvidence{
		SchemaVersion: 1, Scope: "darwinrouter-native-install-migration-rehearsal",
		Release:      releasepack.InstallEvidenceRelease{Version: version, Commit: commit},
		Target:       releasepack.NativeEvidenceTarget{OS: "darwin", Arch: "arm64"},
		Artifact:     releasepack.InstallEvidenceArtifact{Name: "DarwinRouter_1.0.0-rc.11_darwin_arm64.tar.gz", SHA256: digest("3")},
		Installation: releasepack.InstallEvidenceInstall{BinaryVersion: version, PrivatePermissions: "passed", Configuration: "passed", DaemonStart: "passed", ExactWriterStop: "passed"},
		Source:       releasepack.InstallEvidenceSource{Schema: 29, QuickCheck: "ok", Quiescence: "passed"},
		Backup:       releasepack.InstallEvidenceBackup{SHA256: digest("4"), Schema: 29, QuickCheck: "ok"},
		Migration:    releasepack.InstallEvidenceMigration{Schema: stateschema.Current, QuickCheck: "ok", PreservedRecordSHA256: digest("5"), TaskTimingPreserved: "passed", LegacyUsageNotFabricated: "passed"},
		Rollback:     releasepack.InstallEvidenceRollback{DatabaseSHA256: digest("4"), Schema: 29, Pairing: "current_binary_read_only_schema_fixture", BinaryVersion: version, TargetOS: "darwin", TargetArch: "arm64", Smoke: "passed"},
	}
	installBody := canonicalJSON(t, install)
	installSHA := sha256Digest(installBody)
	native := releasepack.NativeEvidence{
		SchemaVersion: 2, Scope: "single_native_target_only", ReleaseVersion: version, SourceCommit: commit,
		Target:    releasepack.NativeEvidenceTarget{OS: "darwin", Arch: "arm64"},
		Toolchain: releasepack.NativeEvidenceGo{GOHOSTOS: "darwin", GOHOSTARCH: "arm64", GOVERSION: "go1.27.1"},
		Gates: []releasepack.NativeEvidenceGate{
			{Name: "source_bound_clean_before", Status: "passed"},
			{Name: "make_check", Status: "passed"},
			{Name: "source_unchanged_after_check", Status: "passed"},
			{Name: "make_qualify_release", Status: "passed"},
			{Name: "source_unchanged_after_qualification", Status: "passed"},
		},
		InstallRehearsal: &releasepack.NativeInstallEvidenceBinding{
			RecordSHA256: installSHA, ArtifactName: install.Artifact.Name,
			ArtifactSHA256: install.Artifact.SHA256, BackupSHA256: install.Backup.SHA256,
			SourceSchema: install.Source.Schema, CurrentSchema: install.Migration.Schema,
		},
	}
	nativeBody := canonicalJSON(t, native)
	directory := t.TempDir()
	nativePath, installPath := filepath.Join(directory, "native.json"), filepath.Join(directory, "install.json")
	if err := os.WriteFile(nativePath, nativeBody, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installPath, installBody, 0600); err != nil {
		t.Fatal(err)
	}
	args := validArgs()
	for flag, value := range map[string]string{
		"--record": nativePath, "--record-sha256": sha256Digest(nativeBody),
		"--install-rehearsal-record": installPath, "--install-rehearsal-record-sha256": installSHA,
	} {
		args = replaceFlagValue(args, flag, value)
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr, releasepack.VerifyNativeEvidenceAgainst); code != 0 || stderr.Len() != 0 {
		t.Fatalf("real verification failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var result releasepack.NativeEvidenceVerification
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.RecordSHA256 != sha256Digest(nativeBody) || result.InstallRehearsalRecordSHA256 != installSHA {
		t.Fatalf("unexpected verification output: result=%#v err=%v", result, err)
	}
	if result.RollbackSchema != 29 {
		t.Fatalf("rollback schema=%d want=29", result.RollbackSchema)
	}
}

func canonicalJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func sha256Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validArgs() []string {
	return []string{
		"--record", "/retained/native.json",
		"--record-sha256", "sha256:" + strings.Repeat("1", 64),
		"--install-rehearsal-record", "/retained/install.json",
		"--install-rehearsal-record-sha256", "sha256:" + strings.Repeat("2", 64),
		"--version", "1.0.0-rc.11",
		"--commit", strings.Repeat("a", 40),
		"--target-os", "darwin",
		"--target-arch", "arm64",
		"--go-version", "go1.27.1",
		"--artifact", "DarwinRouter_1.0.0-rc.11_darwin_arm64.tar.gz",
		"--artifact-sha256", "sha256:" + strings.Repeat("3", 64),
		"--source-schema", "29",
		"--current-schema", strconv.Itoa(stateschema.Current),
		"--backup-sha256", "sha256:" + strings.Repeat("4", 64),
	}
}

func TestRunRequiresAndForwardsIndependentEvidence(t *testing.T) {
	want := releasepack.NativeEvidenceExpectations{
		RecordSHA256:                 "sha256:" + strings.Repeat("1", 64),
		InstallRehearsalRecordSHA256: "sha256:" + strings.Repeat("2", 64),
		Version:                      "1.0.0-rc.11", Commit: strings.Repeat("a", 40),
		TargetOS: "darwin", TargetArch: "arm64", GoVersion: "go1.27.1",
		ArtifactName:   "DarwinRouter_1.0.0-rc.11_darwin_arm64.tar.gz",
		ArtifactSHA256: "sha256:" + strings.Repeat("3", 64),
		SourceSchema:   29, CurrentSchema: stateschema.Current,
		BackupSHA256: "sha256:" + strings.Repeat("4", 64),
	}
	wantResult := releasepack.NativeEvidenceVerification{
		RecordSHA256: want.RecordSHA256, InstallRehearsalRecordSHA256: want.InstallRehearsalRecordSHA256,
		ReleaseVersion: want.Version, SourceCommit: want.Commit,
		TargetOS: want.TargetOS, TargetArch: want.TargetArch, GOVersion: want.GoVersion,
		ArtifactName: want.ArtifactName, ArtifactSHA256: want.ArtifactSHA256,
		SourceSchema: want.SourceSchema, CurrentSchema: want.CurrentSchema, BackupSHA256: want.BackupSHA256,
		RollbackSchema: want.SourceSchema,
	}
	called := false
	var stdout, stderr bytes.Buffer
	code := run(validArgs(), &stdout, &stderr, func(primaryPath, installPath string, got releasepack.NativeEvidenceExpectations) (releasepack.NativeEvidenceVerification, error) {
		called = true
		if primaryPath != "/retained/native.json" || installPath != "/retained/install.json" || !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected verification input: primary=%q install=%q expected=%#v", primaryPath, installPath, got)
		}
		return wantResult, nil
	})
	wantJSON, err := json.Marshal(wantResult)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON = append(wantJSON, '\n')
	if code != 0 || !called || !bytes.Equal(stdout.Bytes(), wantJSON) || stderr.Len() != 0 {
		t.Fatalf("run failed: code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestRunExitSemantics(t *testing.T) {
	verifyOK := func(string, string, releasepack.NativeEvidenceExpectations) (releasepack.NativeEvidenceVerification, error) {
		return releasepack.NativeEvidenceVerification{}, nil
	}
	for name, test := range map[string]struct {
		args []string
		want int
	}{
		"help":       {[]string{"--help"}, 0},
		"unknown":    {[]string{"--unknown"}, 2},
		"missing":    {nil, 2},
		"positional": {append(validArgs(), "extra"), 2},
		"invalid":    {replaceFlagValue(validArgs(), "--go-version", "devel"), 2},
	} {
		t.Run(name, func(t *testing.T) {
			if code := run(test.args, &bytes.Buffer{}, &bytes.Buffer{}, verifyOK); code != test.want {
				t.Fatalf("code=%d want=%d", code, test.want)
			}
		})
	}
	if code := run(validArgs(), &bytes.Buffer{}, &bytes.Buffer{}, func(string, string, releasepack.NativeEvidenceExpectations) (releasepack.NativeEvidenceVerification, error) {
		return releasepack.NativeEvidenceVerification{}, errors.New("denied")
	}); code != 1 {
		t.Fatal("verification failure exit", code)
	}
	if code := run(validArgs(), failWriter{}, &bytes.Buffer{}, verifyOK); code != 1 {
		t.Fatal("output failure exit", code)
	}
	for name, test := range map[string]struct {
		stdout ioWriter
		stderr ioWriter
		verify func(string, string, releasepack.NativeEvidenceExpectations) (releasepack.NativeEvidenceVerification, error)
	}{
		"stdout": {nil, &bytes.Buffer{}, verifyOK},
		"stderr": {&bytes.Buffer{}, nil, verifyOK},
		"verify": {&bytes.Buffer{}, &bytes.Buffer{}, nil},
	} {
		t.Run("nil_"+name, func(t *testing.T) {
			if code := run(validArgs(), test.stdout, test.stderr, test.verify); code != 2 {
				t.Fatalf("code=%d want=2", code)
			}
		})
	}
}

func TestRunDoesNotLeakPathsOrVerifierErrors(t *testing.T) {
	const secret = "/private/release/signing/internal-native.json"
	args := replaceFlagValue(validArgs(), "--record", secret)
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, func(string, string, releasepack.NativeEvidenceExpectations) (releasepack.NativeEvidenceVerification, error) {
		return releasepack.NativeEvidenceVerification{}, errors.New("digest mismatch in " + secret)
	})
	if code != 1 || stdout.Len() != 0 || stderr.String() != "native release evidence verification failed\n" || strings.Contains(stderr.String(), secret) {
		t.Fatalf("unsafe failure: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	args = append(validArgs(), secret)
	stdout.Reset()
	stderr.Reset()
	if code = run(args, &stdout, &stderr, func(string, string, releasepack.NativeEvidenceExpectations) (releasepack.NativeEvidenceVerification, error) {
		t.Fatal("verifier called for positional arguments")
		return releasepack.NativeEvidenceVerification{}, nil
	}); code != 2 || stderr.String() != errUsage.Error()+"\n" || strings.Contains(stderr.String(), secret) {
		t.Fatalf("unsafe usage failure: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// ioWriter is kept local so nil interface values exercise run's public boundary.
type ioWriter interface {
	Write([]byte) (int, error)
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func replaceFlagValue(args []string, name, value string) []string {
	result := append([]string(nil), args...)
	for i := range result {
		if result[i] == name && i+1 < len(result) {
			result[i+1] = value
			return result
		}
	}
	return result
}
