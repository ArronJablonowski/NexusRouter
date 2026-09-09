package releasepack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/stateschema"
)

func validInstallEvidenceFixture() InstallRehearsalEvidence {
	version := "1.0.0-rc.11"
	artifact := "DarwinRouter_" + version + "_darwin_arm64.tar.gz"
	return InstallRehearsalEvidence{
		SchemaVersion: installEvidenceSchema, Scope: installEvidenceScope,
		Release:      InstallEvidenceRelease{Version: version, Commit: strings.Repeat("a", 40)},
		Target:       NativeEvidenceTarget{OS: "darwin", Arch: "arm64"},
		Artifact:     InstallEvidenceArtifact{Name: artifact, SHA256: testInstallDigest("1")},
		Installation: InstallEvidenceInstall{BinaryVersion: version, PrivatePermissions: "passed", Configuration: "passed", DaemonStart: "passed", ExactWriterStop: "passed"},
		Source:       InstallEvidenceSource{Schema: 29, QuickCheck: "ok", Quiescence: "passed"},
		Backup:       InstallEvidenceBackup{SHA256: testInstallDigest("2"), Schema: 29, QuickCheck: "ok"},
		Migration:    InstallEvidenceMigration{Schema: stateschema.Current, QuickCheck: "ok", PreservedRecordSHA256: testInstallDigest("3"), TaskTimingPreserved: "passed", LegacyUsageNotFabricated: "passed"},
		Rollback:     InstallEvidenceRollback{DatabaseSHA256: testInstallDigest("2"), Schema: 29, Pairing: "current_binary_read_only_schema_fixture", BinaryVersion: version, TargetOS: "darwin", TargetArch: "arm64", Smoke: "passed"},
	}
}

func TestInstallRehearsalEvidenceCanonicalContract(t *testing.T) {
	record := validInstallEvidenceFixture()
	body := canonicalInstallEvidence(t, record)
	got, err := ParseInstallRehearsalEvidence(body)
	if err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("canonical record rejected: %v", err)
	}
	for name, mutate := range map[string]func(*InstallRehearsalEvidence){
		"schema":           func(r *InstallRehearsalEvidence) { r.SchemaVersion++ },
		"scope":            func(r *InstallRehearsalEvidence) { r.Scope = "other" },
		"commit":           func(r *InstallRehearsalEvidence) { r.Release.Commit = "short" },
		"target":           func(r *InstallRehearsalEvidence) { r.Target.Arch = "386" },
		"artifact_name":    func(r *InstallRehearsalEvidence) { r.Artifact.Name = "other.tar.gz" },
		"artifact_digest":  func(r *InstallRehearsalEvidence) { r.Artifact.SHA256 = testInstallDigest("x") },
		"permissions":      func(r *InstallRehearsalEvidence) { r.Installation.PrivatePermissions = "claimed" },
		"writer":           func(r *InstallRehearsalEvidence) { r.Installation.ExactWriterStop = "unknown" },
		"quiescence":       func(r *InstallRehearsalEvidence) { r.Source.Quiescence = "failed" },
		"backup_schema":    func(r *InstallRehearsalEvidence) { r.Backup.Schema = 28 },
		"migration_schema": func(r *InstallRehearsalEvidence) { r.Migration.Schema = stateschema.Current - 1 },
		"preservation":     func(r *InstallRehearsalEvidence) { r.Migration.TaskTimingPreserved = "failed" },
		"rollback_digest":  func(r *InstallRehearsalEvidence) { r.Rollback.DatabaseSHA256 = testInstallDigest("4") },
		"rollback_pairing": func(r *InstallRehearsalEvidence) { r.Rollback.Pairing = "prior_release" },
		"rollback_binary":  func(r *InstallRehearsalEvidence) { r.Rollback.BinaryVersion = "1.0.0" },
		"rollback_smoke":   func(r *InstallRehearsalEvidence) { r.Rollback.Smoke = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := record
			mutate(&changed)
			if _, err := ParseInstallRehearsalEvidence(canonicalInstallEvidence(t, changed)); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestInstallRehearsalEvidenceRejectsNoncanonicalAndUnsafeFiles(t *testing.T) {
	body := canonicalInstallEvidence(t, validInstallEvidenceFixture())
	for name, changed := range map[string][]byte{
		"missing_newline": body[:len(body)-1],
		"extra_field":     append(body[:len(body)-2], []byte(",\n  \"extra\": true\n}\n")...),
		"leading_space":   append([]byte(" "), body...),
		"duplicate":       []byte(strings.Replace(string(body), `"scope":`, `"scope":"duplicate", "scope":`, 1)),
		"oversized":       make([]byte, maxInstallEvidence+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInstallRehearsalEvidence(changed); err == nil {
				t.Fatal("noncanonical input accepted")
			}
		})
	}
	root := t.TempDir()
	recordPath := filepath.Join(root, "record.json")
	if err := os.WriteFile(recordPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "record-link.json")
	if err := os.Symlink(recordPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyInstallRehearsalEvidence(link, expectationsForInstall(body)); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := VerifyInstallRehearsalEvidence(root, expectationsForInstall(body)); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestVerifyInstallRehearsalEvidenceBindsAllExpectations(t *testing.T) {
	record := validInstallEvidenceFixture()
	body := canonicalInstallEvidence(t, record)
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	expected := expectationsForInstall(body)
	got, err := VerifyInstallRehearsalEvidence(path, expected)
	if err != nil || got.RecordSHA256 != expected.RecordSHA256 || got.ArtifactSHA256 != expected.ArtifactSHA256 || got.BackupSHA256 != expected.BackupSHA256 {
		t.Fatalf("valid evidence rejected: %#v %v", got, err)
	}
	for name, mutate := range map[string]func(*InstallRehearsalExpectations){
		"record":          func(e *InstallRehearsalExpectations) { e.RecordSHA256 = testInstallDigest("4") },
		"version":         func(e *InstallRehearsalExpectations) { e.Version = "1.0.0" },
		"commit":          func(e *InstallRehearsalExpectations) { e.Commit = strings.Repeat("b", 40) },
		"os":              func(e *InstallRehearsalExpectations) { e.TargetOS = "linux" },
		"arch":            func(e *InstallRehearsalExpectations) { e.TargetArch = "amd64" },
		"artifact":        func(e *InstallRehearsalExpectations) { e.ArtifactName = "DarwinRouter_1.0.0-rc.11_linux_arm64.tar.gz" },
		"artifact_digest": func(e *InstallRehearsalExpectations) { e.ArtifactSHA256 = testInstallDigest("4") },
		"source_schema":   func(e *InstallRehearsalExpectations) { e.SourceSchema = 28 },
		"current_schema":  func(e *InstallRehearsalExpectations) { e.CurrentSchema = stateschema.Current - 1 },
		"backup":          func(e *InstallRehearsalExpectations) { e.BackupSHA256 = testInstallDigest("4") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := expected
			mutate(&changed)
			if _, err := VerifyInstallRehearsalEvidence(path, changed); err == nil {
				t.Fatal("expectation mismatch accepted")
			}
		})
	}
	if err := os.WriteFile(path, append(body, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyInstallRehearsalEvidence(path, expected); err == nil {
		t.Fatal("tampered record accepted")
	}
}

func TestRetainInstallRehearsalEvidenceIsExclusiveOutsideSource(t *testing.T) {
	root := t.TempDir()
	source, evidence := filepath.Join(root, "source"), filepath.Join(root, "evidence")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(evidence, "rehearsal.json")
	record := validInstallEvidenceFixture()
	if err := retainInstallRehearsalEvidence(source, out, record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(out)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("unexpected evidence mode", info, err)
	}
	if err := retainInstallRehearsalEvidence(source, out, record); err == nil {
		t.Fatal("existing record overwritten")
	}
	if err := retainInstallRehearsalEvidence(source, filepath.Join(source, "inside.json"), record); err == nil {
		t.Fatal("record inside checkout accepted")
	}
	linkedParent := filepath.Join(root, "linked-evidence")
	if err := os.Symlink(evidence, linkedParent); err != nil {
		t.Fatal(err)
	}
	if err := retainInstallRehearsalEvidence(source, filepath.Join(linkedParent, "linked.json"), record); err == nil {
		t.Fatal("symlink output parent accepted")
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{source, evidence, "install-rehearsal-token", "Synthetic schema migration evidence"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("private value persisted: %q", forbidden)
		}
	}
}

func canonicalInstallEvidence(t *testing.T, record InstallRehearsalEvidence) []byte {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func expectationsForInstall(body []byte) InstallRehearsalExpectations {
	r := validInstallEvidenceFixture()
	return InstallRehearsalExpectations{
		RecordSHA256: installEvidenceDigest(body), Version: r.Release.Version, Commit: r.Release.Commit,
		TargetOS: r.Target.OS, TargetArch: r.Target.Arch, ArtifactName: r.Artifact.Name,
		ArtifactSHA256: r.Artifact.SHA256, SourceSchema: r.Source.Schema,
		CurrentSchema: r.Migration.Schema, BackupSHA256: r.Backup.SHA256,
	}
}

func testInstallDigest(char string) string { return "sha256:" + strings.Repeat(char, 64) }
