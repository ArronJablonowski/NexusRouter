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

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
)

func TestRunVerifiesCanonicalRecord(t *testing.T) {
	version, commit := "1.0.0-rc.11", strings.Repeat("a", 40)
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	record := releasepack.InstallRehearsalEvidence{
		SchemaVersion: 1, Scope: "nexusrouter-native-install-migration-rehearsal",
		Release:      releasepack.InstallEvidenceRelease{Version: version, Commit: commit},
		Target:       releasepack.NativeEvidenceTarget{OS: "darwin", Arch: "arm64"},
		Artifact:     releasepack.InstallEvidenceArtifact{Name: "NexusRouter_1.0.0-rc.11_darwin_arm64.tar.gz", SHA256: digest("1")},
		Installation: releasepack.InstallEvidenceInstall{BinaryVersion: version, PrivatePermissions: "passed", Configuration: "passed", DaemonStart: "passed", ExactWriterStop: "passed"},
		Source:       releasepack.InstallEvidenceSource{Schema: 29, QuickCheck: "ok", Quiescence: "passed"},
		Backup:       releasepack.InstallEvidenceBackup{SHA256: digest("2"), Schema: 29, QuickCheck: "ok"},
		Migration:    releasepack.InstallEvidenceMigration{Schema: stateschema.Current, QuickCheck: "ok", PreservedRecordSHA256: digest("3"), TaskTimingPreserved: "passed", LegacyUsageNotFabricated: "passed"},
		Rollback:     releasepack.InstallEvidenceRollback{DatabaseSHA256: digest("2"), Schema: 29, Pairing: "current_binary_read_only_schema_fixture", BinaryVersion: version, TargetOS: "darwin", TargetArch: "arm64", Smoke: "passed"},
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	path := filepath.Join(t.TempDir(), "rehearsal.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	args := []string{"--record", path, "--record-sha256", "sha256:" + hex.EncodeToString(sum[:]), "--version", version, "--commit", commit,
		"--target-os", "darwin", "--target-arch", "arm64", "--artifact", record.Artifact.Name, "--artifact-sha256", record.Artifact.SHA256,
		"--source-schema", "29", "--current-schema", strconv.Itoa(stateschema.Current), "--backup-sha256", record.Backup.SHA256}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr, releasepack.VerifyInstallRehearsalEvidence); code != 0 || !strings.Contains(stdout.String(), `"rollback_schema":29`) || stderr.Len() != 0 {
		t.Fatalf("real verification failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunRequiresAndForwardsIndependentExpectations(t *testing.T) {
	args := []string{
		"--record", "/evidence/rehearsal.json", "--record-sha256", "sha256:" + strings.Repeat("1", 64),
		"--version", "1.0.0-rc.11", "--commit", strings.Repeat("a", 40),
		"--target-os", "darwin", "--target-arch", "arm64",
		"--artifact", "NexusRouter_1.0.0-rc.11_darwin_arm64.tar.gz",
		"--artifact-sha256", "sha256:" + strings.Repeat("2", 64),
		"--source-schema", "29", "--current-schema", strconv.Itoa(stateschema.Current),
		"--backup-sha256", "sha256:" + strings.Repeat("3", 64),
	}
	want := releasepack.InstallRehearsalExpectations{
		RecordSHA256: "sha256:" + strings.Repeat("1", 64), Version: "1.0.0-rc.11", Commit: strings.Repeat("a", 40),
		TargetOS: "darwin", TargetArch: "arm64", ArtifactName: "NexusRouter_1.0.0-rc.11_darwin_arm64.tar.gz",
		ArtifactSHA256: "sha256:" + strings.Repeat("2", 64), SourceSchema: 29, CurrentSchema: stateschema.Current,
		BackupSHA256: "sha256:" + strings.Repeat("3", 64),
	}
	called := false
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, func(path string, got releasepack.InstallRehearsalExpectations) (releasepack.InstallRehearsalVerification, error) {
		called = true
		if path != "/evidence/rehearsal.json" || !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected verification input: %q %#v", path, got)
		}
		return releasepack.InstallRehearsalVerification{RecordSHA256: got.RecordSHA256, ReleaseVersion: got.Version}, nil
	})
	if code != 0 || !called || !strings.Contains(stdout.String(), `"release_version":"1.0.0-rc.11"`) || stderr.Len() != 0 {
		t.Fatalf("run failed: code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestRunExitSemantics(t *testing.T) {
	valid := []string{
		"--record", "/evidence/rehearsal.json", "--record-sha256", "sha256:" + strings.Repeat("1", 64),
		"--version", "1.0.0", "--commit", strings.Repeat("a", 40), "--target-os", "darwin", "--target-arch", "arm64",
		"--artifact", "NexusRouter_1.0.0_darwin_arm64.tar.gz", "--artifact-sha256", "sha256:" + strings.Repeat("2", 64),
		"--source-schema", "29", "--current-schema", strconv.Itoa(stateschema.Current), "--backup-sha256", "sha256:" + strings.Repeat("3", 64),
	}
	verifyOK := func(string, releasepack.InstallRehearsalExpectations) (releasepack.InstallRehearsalVerification, error) {
		return releasepack.InstallRehearsalVerification{}, nil
	}
	for name, args := range map[string][]string{
		"help": {"--help"}, "unknown": {"--unknown"}, "missing": nil,
		"extra":   append(append([]string(nil), valid...), "extra"),
		"invalid": append(append([]string(nil), valid[:5]...), append([]string{"bad"}, valid[6:]...)...),
	} {
		want := 2
		if name == "help" {
			want = 0
		}
		if code := run(args, &bytes.Buffer{}, &bytes.Buffer{}, verifyOK); code != want {
			t.Fatalf("%s: code=%d want=%d", name, code, want)
		}
	}
	if code := run(valid, &bytes.Buffer{}, &bytes.Buffer{}, func(string, releasepack.InstallRehearsalExpectations) (releasepack.InstallRehearsalVerification, error) {
		return releasepack.InstallRehearsalVerification{}, errors.New("denied")
	}); code != 1 {
		t.Fatal("verification failure exit", code)
	}
	if code := run(valid, failWriter{}, &bytes.Buffer{}, verifyOK); code != 1 {
		t.Fatal("output failure exit", code)
	}
	if code := run(valid, nil, &bytes.Buffer{}, verifyOK); code != 2 {
		t.Fatal("nil output exit", code)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
