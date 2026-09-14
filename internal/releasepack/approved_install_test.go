package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestVerifyApprovedInstallBindsNativeExecutionAndApproval(t *testing.T) {
	options := approvedInstallFixture(t)
	receipt, err := VerifyApprovedInstall(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ReleaseVersion != "1.0.0" || receipt.TargetOS != runtime.GOOS || receipt.TargetArch != runtime.GOARCH ||
		receipt.SourceCommit == "" || receipt.ManifestSHA256 == "" || receipt.ArtifactSHA256 == "" ||
		receipt.InstalledBinarySHA256 == "" || receipt.VersionOutput != "darwin 1.0.0" ||
		receipt.ApprovalVerification.CandidateRecordSHA256 != options.Verification.ExpectedCandidateSHA256 ||
		receipt.VerifierID != options.VerifierID || receipt.HostID != options.HostID ||
		receipt.PublicKeyChannel != options.PublicKeyChannel || receipt.VerifiedAt != "2026-09-14T12:34:56Z" || receipt.Result != "passed" {
		t.Fatal("incomplete approved native verification receipt", receipt)
	}
	installed := filepath.Join(options.InstallRoot, "bin", "darwin")
	info, err := os.Lstat(installed)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0755 {
		t.Fatal("native binary was not installed privately", info, err)
	}
	body, err := MarshalApprovedInstallVerificationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseApprovedInstallVerificationReceipt(body)
	if err != nil || parsed != receipt {
		t.Fatal("canonical receipt rejected", parsed, err)
	}
}

func TestVerifyApprovedInstallRejectsTamperingAndUnsafeInputs(t *testing.T) {
	t.Run("wrong_native_version", func(t *testing.T) {
		signing, _ := approvedSigningFixtureWithExecutableVersion(t, false, "1.0.1", "")
		if err := SignApproved(context.Background(), signing); err != nil {
			t.Fatal(err)
		}
		options := approvedInstallOptions(t, signing)
		if receipt, err := VerifyApprovedInstall(context.Background(), options); err == nil || receipt.SchemaVersion != 0 {
			t.Fatal("wrong native version accepted", receipt, err)
		}
	})
	t.Run("signed_archive_tampering", func(t *testing.T) {
		options := approvedInstallFixture(t)
		archive := filepath.Join(options.Verification.Dir, "DarwinRouter_1.0.0_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
		appendPublishedFixtureByte(t, archive, 0)
		if receipt, err := VerifyApprovedInstall(context.Background(), options); err == nil || receipt.SchemaVersion != 0 {
			t.Fatal("tampered signed archive executed", receipt, err)
		}
		if _, err := os.Lstat(options.InstallRoot); !os.IsNotExist(err) {
			t.Fatal("failed approval created install root", err)
		}
	})
	for _, scenario := range []string{"wrong_runtime", "existing_install", "inside_release", "inside_source", "writable_parent", "invalid_verifier", "invalid_host", "invalid_channel", "fractional_time"} {
		t.Run(scenario, func(t *testing.T) {
			options := approvedInstallFixture(t)
			switch scenario {
			case "wrong_runtime":
				if runtime.GOARCH == "arm64" {
					options.TargetArch = "amd64"
				} else {
					options.TargetArch = "arm64"
				}
			case "existing_install":
				if err := os.Mkdir(options.InstallRoot, 0700); err != nil {
					t.Fatal(err)
				}
			case "inside_release":
				options.InstallRoot = filepath.Join(options.Verification.Dir, "installed")
			case "inside_source":
				options.InstallRoot = filepath.Join(options.Verification.Source, "installed")
			case "writable_parent":
				parent := t.TempDir()
				if err := os.Chmod(parent, 0777); err != nil {
					t.Fatal(err)
				}
				options.InstallRoot = filepath.Join(parent, "installed")
			case "invalid_verifier":
				options.VerifierID = "bad verifier"
			case "invalid_host":
				options.HostID = "x"
			case "invalid_channel":
				options.PublicKeyChannel = "http://example.invalid/key"
			case "fractional_time":
				options.Now = func() time.Time { return time.Date(2026, 9, 14, 12, 34, 56, 1, time.UTC) }
			}
			if receipt, err := VerifyApprovedInstall(context.Background(), options); err == nil || receipt.SchemaVersion != 0 {
				t.Fatal("unsafe approved install accepted", receipt, err)
			}
			if scenario == "inside_source" {
				if _, err := os.Lstat(options.InstallRoot); !os.IsNotExist(err) {
					t.Fatal("source-overlapping install created state", err)
				}
				status, err := command(context.Background(), options.Verification.Source, environment(), "git", "status", "--porcelain=v1", "--untracked-files=all")
				if err != nil || status != "" {
					t.Fatal("source-overlapping install dirtied checkout", status, err)
				}
			}
		})
	}
}

func TestApprovedInstallPathsRejectSymmetricOverlap(t *testing.T) {
	protected := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	if approvedInstallPathsDisjoint(filepath.Join(protected, "installed"), protected) ||
		approvedInstallPathsDisjoint(filepath.Dir(protected), protected) ||
		!approvedInstallPathsDisjoint(filepath.Join(t.TempDir(), "installed"), protected) {
		t.Fatal("install/protected-root overlap classification failed")
	}
}

func TestApprovedInstallReceiptRejectsFieldAndEncodingTampering(t *testing.T) {
	options := approvedInstallFixture(t)
	receipt, err := VerifyApprovedInstall(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ApprovedInstallVerificationReceipt){
		"version":  func(r *ApprovedInstallVerificationReceipt) { r.ReleaseVersion = "1.0.1" },
		"commit":   func(r *ApprovedInstallVerificationReceipt) { r.SourceCommit = "bad" },
		"manifest": func(r *ApprovedInstallVerificationReceipt) { r.ManifestSHA256 = "bad" },
		"artifact": func(r *ApprovedInstallVerificationReceipt) { r.ArtifactName = "other.tar.gz" },
		"binary":   func(r *ApprovedInstallVerificationReceipt) { r.InstalledBinarySHA256 = "bad" },
		"output":   func(r *ApprovedInstallVerificationReceipt) { r.VersionOutput = "darwin 1.0.1" },
		"approval": func(r *ApprovedInstallVerificationReceipt) { r.ApprovalVerification.KeyID = "bad key" },
		"verifier": func(r *ApprovedInstallVerificationReceipt) { r.VerifierID = "bad verifier" },
		"host":     func(r *ApprovedInstallVerificationReceipt) { r.HostID = "x" },
		"channel":  func(r *ApprovedInstallVerificationReceipt) { r.PublicKeyChannel = "file:///tmp/key" },
		"time":     func(r *ApprovedInstallVerificationReceipt) { r.VerifiedAt = "2026-09-14T12:34:56.1Z" },
		"result":   func(r *ApprovedInstallVerificationReceipt) { r.Result = "failed" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			body, marshalErr := json.MarshalIndent(changed, "", "  ")
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err := ParseApprovedInstallVerificationReceipt(append(body, '\n')); err == nil {
				t.Fatal("tampered receipt accepted")
			}
		})
	}
	body, err := MarshalApprovedInstallVerificationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseApprovedInstallVerificationReceipt(append([]byte(" "), body...)); err == nil {
		t.Fatal("noncanonical receipt accepted")
	}
}

func approvedInstallFixture(t *testing.T) ApprovedInstallVerificationOptions {
	t.Helper()
	signing, _ := approvedSigningExecutableFixture(t)
	if err := SignApproved(context.Background(), signing); err != nil {
		t.Fatal(err)
	}
	return approvedInstallOptions(t, signing)
}

func approvedInstallOptions(t *testing.T, signing ApprovedSigningOptions) ApprovedInstallVerificationOptions {
	t.Helper()
	return ApprovedInstallVerificationOptions{
		Verification: verificationOptions(signing), TargetOS: runtime.GOOS, TargetArch: runtime.GOARCH,
		InstallRoot: filepath.Join(t.TempDir(), "installed"), VerifierID: "idp:independent-verifier",
		HostID: "host:darwin-verifier-01", PublicKeyChannel: "https://keys.example.invalid/darwinrouter/release-test-01.json",
		Now: func() time.Time { return time.Date(2026, 9, 14, 12, 34, 56, 0, time.UTC) },
	}
}
