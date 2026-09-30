package releasepack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
)

func nativeBundleFixture(t *testing.T) (string, string, NativeEvidenceExpectations) {
	t.Helper()
	directory := t.TempDir()
	version := "1.2.3-rc.4"
	commit := "0123456789abcdef0123456789abcdef01234567"
	targetOS, targetArch := "linux", "amd64"
	install := validInstallEvidenceFixture()
	install.Release = InstallEvidenceRelease{Version: version, Commit: commit}
	install.Target = NativeEvidenceTarget{OS: targetOS, Arch: targetArch}
	install.Artifact.Name = "NexusRouter_" + version + "_" + targetOS + "_" + targetArch + ".tar.gz"
	install.Installation.BinaryVersion = version
	install.Rollback.BinaryVersion = version
	install.Rollback.TargetOS, install.Rollback.TargetArch = targetOS, targetArch
	installBody := canonicalInstallEvidence(t, install)
	installPath := filepath.Join(directory, "install.json")
	if err := os.WriteFile(installPath, installBody, 0600); err != nil {
		t.Fatal(err)
	}
	installDigest := installEvidenceDigest(installBody)
	native := NativeEvidence{
		SchemaVersion: 2, Scope: "single_native_target_only", ReleaseVersion: version, SourceCommit: commit,
		Target:    NativeEvidenceTarget{OS: targetOS, Arch: targetArch},
		Toolchain: NativeEvidenceGo{GOHOSTOS: targetOS, GOHOSTARCH: targetArch, GOVERSION: "go1.27.1"},
		Gates:     append([]NativeEvidenceGate(nil), nativeEvidenceGates...),
		InstallRehearsal: &NativeInstallEvidenceBinding{
			RecordSHA256: installDigest, ArtifactName: install.Artifact.Name, ArtifactSHA256: install.Artifact.SHA256,
			BackupSHA256: install.Backup.SHA256, SourceSchema: install.Source.Schema, CurrentSchema: install.Migration.Schema,
		},
	}
	nativeBody := nativeEvidenceBody(t, native)
	nativePath := filepath.Join(directory, "native.json")
	if err := os.WriteFile(nativePath, nativeBody, 0644); err != nil {
		t.Fatal(err)
	}
	return nativePath, installPath, NativeEvidenceExpectations{
		RecordSHA256: nativeEvidenceDigest(nativeBody), InstallRehearsalRecordSHA256: installDigest,
		Version: version, Commit: commit, TargetOS: targetOS, TargetArch: targetArch, GoVersion: "go1.27.1",
		ArtifactName: install.Artifact.Name, ArtifactSHA256: install.Artifact.SHA256,
		SourceSchema: install.Source.Schema, CurrentSchema: install.Migration.Schema, BackupSHA256: install.Backup.SHA256,
	}
}

func TestVerifyNativeEvidenceAgainstAcceptsExactOfflineBundle(t *testing.T) {
	nativePath, installPath, expected := nativeBundleFixture(t)
	if err := expected.Validate(); err != nil {
		t.Fatal("valid expectations rejected", err)
	}
	got, err := VerifyNativeEvidenceAgainst(nativePath, installPath, expected)
	if err != nil {
		t.Fatal("valid native evidence bundle rejected", err)
	}
	want := NativeEvidenceVerification{
		RecordSHA256: expected.RecordSHA256, InstallRehearsalRecordSHA256: expected.InstallRehearsalRecordSHA256,
		ReleaseVersion: expected.Version, SourceCommit: expected.Commit, TargetOS: expected.TargetOS, TargetArch: expected.TargetArch,
		GOVersion: expected.GoVersion, ArtifactName: expected.ArtifactName, ArtifactSHA256: expected.ArtifactSHA256,
		SourceSchema: expected.SourceSchema, CurrentSchema: expected.CurrentSchema, BackupSHA256: expected.BackupSHA256,
		RollbackSchema: expected.SourceSchema,
	}
	if got != want {
		t.Fatalf("verification result mismatch:\n got %#v\nwant %#v", got, want)
	}
	body, err := json.Marshal(got)
	if err != nil || string(body) != `{"record_sha256":"`+expected.RecordSHA256+`","install_rehearsal_record_sha256":"`+expected.InstallRehearsalRecordSHA256+`","release_version":"1.2.3-rc.4","source_commit":"0123456789abcdef0123456789abcdef01234567","target_os":"linux","target_arch":"amd64","go_version":"go1.27.1","artifact_name":"`+expected.ArtifactName+`","artifact_sha256":"`+expected.ArtifactSHA256+`","source_schema":29,"current_schema":`+strconv.Itoa(stateschema.Current)+`,"backup_sha256":"`+expected.BackupSHA256+`","rollback_schema":29}` {
		t.Fatal("unexpected verification JSON contract", string(body), err)
	}
}

func TestVerifyNativeEvidenceAgainstRejectsExpectationDrift(t *testing.T) {
	nativePath, installPath, base := nativeBundleFixture(t)
	tests := map[string]func(*NativeEvidenceExpectations){
		"record_digest":          func(e *NativeEvidenceExpectations) { e.RecordSHA256 = testInstallDigest("9") },
		"companion_digest":       func(e *NativeEvidenceExpectations) { e.InstallRehearsalRecordSHA256 = testInstallDigest("8") },
		"version":                func(e *NativeEvidenceExpectations) { e.Version = "1.2.4" },
		"commit":                 func(e *NativeEvidenceExpectations) { e.Commit = strings.Repeat("a", 40) },
		"target_os":              func(e *NativeEvidenceExpectations) { e.TargetOS = "darwin" },
		"target_arch":            func(e *NativeEvidenceExpectations) { e.TargetArch = "arm64" },
		"go_version":             func(e *NativeEvidenceExpectations) { e.GoVersion = "go1.27.2" },
		"artifact_name":          func(e *NativeEvidenceExpectations) { e.ArtifactName = "NexusRouter_1.2.4_linux_amd64.tar.gz" },
		"artifact_digest":        func(e *NativeEvidenceExpectations) { e.ArtifactSHA256 = testInstallDigest("7") },
		"install_source_schema":  func(e *NativeEvidenceExpectations) { e.SourceSchema = 28 },
		"install_current_schema": func(e *NativeEvidenceExpectations) { e.CurrentSchema = stateschema.Current - 1 },
		"install_backup_digest":  func(e *NativeEvidenceExpectations) { e.BackupSHA256 = testInstallDigest("6") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			expected := base
			mutate(&expected)
			if got, err := VerifyNativeEvidenceAgainst(nativePath, installPath, expected); err == nil || got != (NativeEvidenceVerification{}) {
				t.Fatal("expectation drift accepted", got, err)
			}
		})
	}
}

func TestVerifyNativeEvidenceAgainstRequiresSchemaTwoAndExactCompanion(t *testing.T) {
	nativePath, installPath, expected := nativeBundleFixture(t)
	for name, change := range map[string]func(*NativeEvidence, *InstallRehearsalEvidence, *NativeEvidenceExpectations){
		"schema_one": func(native *NativeEvidence, _ *InstallRehearsalEvidence, _ *NativeEvidenceExpectations) {
			native.SchemaVersion, native.InstallRehearsal = 1, nil
		},
		"primary_binding": func(native *NativeEvidence, _ *InstallRehearsalEvidence, _ *NativeEvidenceExpectations) {
			native.InstallRehearsal.BackupSHA256 = testInstallDigest("5")
		},
		"companion_validation": func(native *NativeEvidence, install *InstallRehearsalEvidence, expected *NativeEvidenceExpectations) {
			install.Installation.DaemonStart = "failed"
			changedInstallBody, err := json.MarshalIndent(install, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			changedInstallBody = append(changedInstallBody, '\n')
			expected.InstallRehearsalRecordSHA256 = installEvidenceDigest(changedInstallBody)
			native.InstallRehearsal.RecordSHA256 = expected.InstallRehearsalRecordSHA256
		},
	} {
		t.Run(name, func(t *testing.T) {
			nativeBody, err := os.ReadFile(nativePath)
			if err != nil {
				t.Fatal(err)
			}
			var native NativeEvidence
			if err = json.Unmarshal(nativeBody, &native); err != nil {
				t.Fatal(err)
			}
			installBody, err := os.ReadFile(installPath)
			if err != nil {
				t.Fatal(err)
			}
			var install InstallRehearsalEvidence
			if err = json.Unmarshal(installBody, &install); err != nil {
				t.Fatal(err)
			}
			caseExpected := expected
			change(&native, &install, &caseExpected)
			dir := t.TempDir()
			changedNative := filepath.Join(dir, "native.json")
			changedInstall := filepath.Join(dir, "install.json")
			changedNativeBody := nativeEvidenceBody(t, native)
			caseExpected.RecordSHA256 = nativeEvidenceDigest(changedNativeBody)
			if err = os.WriteFile(changedNative, changedNativeBody, 0644); err != nil {
				t.Fatal(err)
			}
			changedInstallBody, err := json.MarshalIndent(install, "", "  ")
			if err != nil || os.WriteFile(changedInstall, append(changedInstallBody, '\n'), 0600) != nil {
				t.Fatal("write changed companion", err)
			}
			if _, err = VerifyNativeEvidenceAgainst(changedNative, changedInstall, caseExpected); err == nil {
				t.Fatal("invalid native bundle accepted")
			}
		})
	}
}

func TestNativeEvidenceExpectationsRejectMalformedValues(t *testing.T) {
	_, _, base := nativeBundleFixture(t)
	for name, mutate := range map[string]func(*NativeEvidenceExpectations){
		"empty":          func(e *NativeEvidenceExpectations) { *e = NativeEvidenceExpectations{} },
		"digest_case":    func(e *NativeEvidenceExpectations) { e.RecordSHA256 = "SHA256:" + strings.Repeat("0", 64) },
		"unsupported_os": func(e *NativeEvidenceExpectations) { e.TargetOS = "windows" },
		"cross_artifact": func(e *NativeEvidenceExpectations) { e.ArtifactName = "README.md" },
		"toolchain":      func(e *NativeEvidenceExpectations) { e.GoVersion = "devel go1.27" },
	} {
		t.Run(name, func(t *testing.T) {
			expected := base
			mutate(&expected)
			if expected.Validate() == nil {
				t.Fatal("malformed expectations accepted")
			}
		})
	}
}
