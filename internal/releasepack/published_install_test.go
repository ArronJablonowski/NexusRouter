package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPublishedInstallEvidenceBindsDownloadedArchiveAndReceipt(t *testing.T) {
	receiptFile, installFile, expected := publishedInstallFixture(t)
	evidence, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected)
	if err != nil || evidence.PublicationReceiptSHA256 != expected.PublicationReceiptSHA256 ||
		evidence.InstallEvidenceSHA256 != expected.InstallEvidenceSHA256 || evidence.ArtifactSHA256 == "" || evidence.CurrentSchema != 32 ||
		evidence.VerifiedAt != "2026-09-07T01:02:00Z" {
		t.Fatal("published install evidence rejected", evidence, err)
	}
	body, err := MarshalPublishedInstallEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePublishedInstallEvidence(body)
	if err != nil || parsed != evidence {
		t.Fatal("canonical published install evidence rejected", parsed, err)
	}
}

func TestPublishedInstallEvidenceUsesExactModesUnderRestrictiveUmask(t *testing.T) {
	if os.Getenv("DARWINROUTER_PUBLISHED_INSTALL_UMASK_HELPER") == "1" {
		syscall.Umask(0077)
		receiptFile, installFile, expected := publishedInstallFixture(t)
		if _, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(expected.InstallRoot, "bin", "darwin")
		info, err := os.Lstat(binary)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatal("restrictive umask changed installed mode", info, err)
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPublishedInstallEvidenceUsesExactModesUnderRestrictiveUmask$")
	command.Env = append(os.Environ(), "DARWINROUTER_PUBLISHED_INSTALL_UMASK_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restrictive-umask subprocess failed: %v\n%s", err, output)
	}
}

func TestPublishedInstallEvidenceRejectsUnboundOrAdversarialInputs(t *testing.T) {
	receiptFile, installFile, valid := publishedInstallFixture(t)
	cases := map[string]func(*PublishedInstallExpectations){
		"receipt_digest": func(e *PublishedInstallExpectations) { e.PublicationReceiptSHA256 = testInstallDigest("a") },
		"install_digest": func(e *PublishedInstallExpectations) { e.InstallEvidenceSHA256 = testInstallDigest("b") },
		"backup_digest":  func(e *PublishedInstallExpectations) { e.BackupSHA256 = testInstallDigest("c") },
		"target_os":      func(e *PublishedInstallExpectations) { e.TargetOS = "windows" },
		"target_arch":    func(e *PublishedInstallExpectations) { e.TargetArch = "386" },
		"verifier":       func(e *PublishedInstallExpectations) { e.VerifierID = "bad verifier" },
		"missing_clock":  func(e *PublishedInstallExpectations) { e.Now = nil },
		"fractional_time": func(e *PublishedInstallExpectations) {
			e.Now = func() time.Time { return time.Date(2026, 9, 7, 1, 2, 0, 1, time.UTC) }
		},
		"before_receipt": func(e *PublishedInstallExpectations) {
			e.Now = func() time.Time { return time.Date(2026, 9, 7, 0, 59, 59, 0, time.UTC) }
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			expected := valid
			mutate(&expected)
			if got, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected); err == nil || got.SchemaVersion != 0 {
				t.Fatal("unbound published install evidence accepted", got, err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreatePublishedInstallEvidence(cancelled, receiptFile, installFile, valid); err == nil {
		t.Fatal("cancelled verification accepted")
	}
	link := filepath.Join(t.TempDir(), "install-link.json")
	if err := os.Symlink(installFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, link, valid); err == nil {
		t.Fatal("symlinked install evidence accepted")
	}
}

func TestPublishedInstallEvidenceRejectsCanonicalFieldTampering(t *testing.T) {
	receiptFile, installFile, expected := publishedInstallFixture(t)
	evidence, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PublishedInstallEvidence){
		"version":     func(e *PublishedInstallEvidence) { e.ReleaseVersion = "1.0.1" },
		"artifact":    func(e *PublishedInstallEvidence) { e.ArtifactName = "other.tar.gz" },
		"binary_mode": func(e *PublishedInstallEvidence) { e.InstalledBinaryMode = "0777" },
		"version_output": func(e *PublishedInstallEvidence) {
			e.VersionOutput = "darwin other"
		},
		"schema": func(e *PublishedInstallEvidence) { e.CurrentSchema = 31 },
		"time":   func(e *PublishedInstallEvidence) { e.VerifiedAt = "2026-09-07T00:00:00Z" },
		"fractional_time": func(e *PublishedInstallEvidence) {
			e.VerifiedAt = "2026-09-07T01:02:00.5Z"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := evidence
			mutate(&changed)
			body, marshalErr := json.MarshalIndent(changed, "", "  ")
			body = append(body, '\n')
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err := ParsePublishedInstallEvidence(body); err == nil {
				t.Fatal("tampered canonical evidence accepted")
			}
		})
	}
	body, err := MarshalPublishedInstallEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePublishedInstallEvidence(append([]byte(" "), body...)); err == nil {
		t.Fatal("noncanonical JSON accepted")
	}
}

func publishedInstallFixture(t *testing.T) (string, string, PublishedInstallExpectations) {
	t.Helper()
	preflight, signedDir := publishedExecutableFixture(t)
	downloadDir := filepath.Join(t.TempDir(), "download")
	receipt, err := VerifyPublishedRelease(t.Context(), &fixtureReleaseReader{source: signedDir}, PublishedVerificationOptions{
		Preflight: preflight, DownloadDir: downloadDir, VerifierID: "idp:release-verifier",
	})
	if err != nil {
		t.Fatal(err)
	}
	receiptBody, err := MarshalPostPublicationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptFile := filepath.Join(t.TempDir(), "post-publication.json")
	if err = os.WriteFile(receiptFile, receiptBody, 0600); err != nil {
		t.Fatal(err)
	}
	targetOS, targetArch := runtime.GOOS, runtime.GOARCH
	artifactName := "DarwinRouter_" + receipt.ReleaseVersion + "_" + targetOS + "_" + targetArch + ".tar.gz"
	artifactSHA := receiptAssetSHA(receipt.Assets, artifactName)
	install := validInstallEvidenceFixture()
	install.Release = InstallEvidenceRelease{Version: receipt.ReleaseVersion, Commit: receipt.SourceCommit}
	install.Target = NativeEvidenceTarget{OS: targetOS, Arch: targetArch}
	install.Artifact = InstallEvidenceArtifact{Name: artifactName, SHA256: artifactSHA}
	install.Installation.BinaryVersion = receipt.ReleaseVersion
	install.Rollback.BinaryVersion, install.Rollback.TargetOS, install.Rollback.TargetArch = receipt.ReleaseVersion, targetOS, targetArch
	installBody := canonicalInstallEvidence(t, install)
	installFile := filepath.Join(t.TempDir(), "published-install.json")
	if err = os.WriteFile(installFile, installBody, 0600); err != nil {
		t.Fatal(err)
	}
	return receiptFile, installFile, PublishedInstallExpectations{
		PublicationReceiptSHA256: rollbackDigest(receiptBody), InstallEvidenceSHA256: installEvidenceDigest(installBody),
		TargetOS: targetOS, TargetArch: targetArch, BackupSHA256: install.Backup.SHA256,
		VerifierID: "idp:published-install-verifier", Now: func() time.Time {
			return time.Date(2026, 9, 7, 1, 2, 0, 0, time.UTC)
		},
		DownloadDir: downloadDir, InstallRoot: filepath.Join(t.TempDir(), "installed"),
	}
}

func TestPublishedInstallEvidenceRejectsReceiptInstallArtifactMismatch(t *testing.T) {
	receiptFile, installFile, expected := publishedInstallFixture(t)
	body, err := os.ReadFile(installFile)
	if err != nil {
		t.Fatal(err)
	}
	var install InstallRehearsalEvidence
	if err = json.Unmarshal(body, &install); err != nil {
		t.Fatal(err)
	}
	install.Artifact.SHA256 = "sha256:" + strings.Repeat("f", 64)
	tampered := canonicalInstallEvidence(t, install)
	if err = os.WriteFile(installFile, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	expected.InstallEvidenceSHA256 = installEvidenceDigest(tampered)
	if _, err = CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected); err == nil {
		t.Fatal("install evidence for different bytes accepted")
	}
}

func TestPublishedInstallEvidenceRejectsChangedDownloadsAndUnsafeInstallRoots(t *testing.T) {
	for _, scenario := range []string{"archive", "manifest", "sums", "preexisting_install", "install_inside_download", "writable_parent", "symlink_parent", "valid_non_native_target"} {
		t.Run(scenario, func(t *testing.T) {
			receiptFile, installFile, expected := publishedInstallFixture(t)
			switch scenario {
			case "archive":
				name := "DarwinRouter_1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
				appendPublishedFixtureByte(t, filepath.Join(expected.DownloadDir, name), 0)
			case "manifest":
				appendPublishedFixtureByte(t, filepath.Join(expected.DownloadDir, "manifest.json"), ' ')
			case "sums":
				appendPublishedFixtureByte(t, filepath.Join(expected.DownloadDir, "SHA256SUMS"), ' ')
			case "preexisting_install":
				if err := os.Mkdir(expected.InstallRoot, 0700); err != nil {
					t.Fatal(err)
				}
			case "install_inside_download":
				expected.InstallRoot = filepath.Join(expected.DownloadDir, "installed")
			case "writable_parent":
				parent := t.TempDir()
				if err := os.Chmod(parent, 0777); err != nil {
					t.Fatal(err)
				}
				expected.InstallRoot = filepath.Join(parent, "installed")
			case "symlink_parent":
				realParent := t.TempDir()
				link := filepath.Join(t.TempDir(), "parent-link")
				if err := os.Symlink(realParent, link); err != nil {
					t.Fatal(err)
				}
				expected.InstallRoot = filepath.Join(link, "installed")
			case "valid_non_native_target":
				if runtime.GOARCH == "arm64" {
					expected.TargetArch = "amd64"
				} else {
					expected.TargetArch = "arm64"
				}
			}
			if got, err := CreatePublishedInstallEvidence(context.Background(), receiptFile, installFile, expected); err == nil || got.SchemaVersion != 0 {
				t.Fatal("changed or unsafe published install accepted", got, err)
			}
		})
	}
}

func appendPublishedFixtureByte(t *testing.T, path string, value byte) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(body, value), 0644); err != nil {
		t.Fatal(err)
	}
}
