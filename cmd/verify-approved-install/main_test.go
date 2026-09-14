package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestRunReservesThenRetainsApprovedInstallReceipt(t *testing.T) {
	args, out := approvedInstallArgs(t)
	when := time.Date(2026, 9, 14, 12, 34, 56, 987, time.FixedZone("offset", -6*60*60))
	var captured releasepack.ApprovedInstallVerificationOptions
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr, func() time.Time { return when }, func(_ context.Context, options releasepack.ApprovedInstallVerificationOptions) (releasepack.ApprovedInstallVerificationReceipt, error) {
		captured = options
		info, err := os.Stat(out)
		if err != nil || info.Size() != 0 || info.Mode().Perm() != 0600 {
			t.Fatal("output was not privately reserved before verification", info, err)
		}
		return approvedInstallReceiptFixture(), nil
	})
	if code != 0 || stderr.Len() != 0 || captured.Verification.Dir == "" || captured.Verification.Source == "" ||
		captured.Verification.CandidateRecordFile != "/evidence/candidate.json" ||
		captured.Verification.ExpectedCandidateSHA256 != "sha256:"+strings.Repeat("a", 64) ||
		captured.Verification.LicenseEvidenceFile != "/evidence/license.json" ||
		captured.Verification.TrustRecordFile != "/evidence/trust.json" ||
		captured.Verification.AuthorizationRecordFile != "/evidence/authorization.json" ||
		captured.Verification.ExpectedKeyID != "release-test-01" ||
		captured.TargetOS != "darwin" || captured.TargetArch != "arm64" || captured.InstallRoot == "" ||
		captured.VerifierID != "idp:install-verifier" || captured.HostID != "host:darwin-arm64" ||
		captured.PublicKeyChannel != "https://keys.example.invalid/darwinrouter" ||
		!captured.Now().Equal(time.Date(2026, 9, 14, 18, 34, 56, 0, time.UTC)) {
		t.Fatalf("arguments not forwarded: code=%d options=%+v stderr=%q", code, captured, stderr.String())
	}
	digest := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatal("receipt digest not emitted", stdout.String())
	}
	receipt, err := releasepack.VerifyApprovedInstallVerificationReceipt(out, digest)
	if err != nil || receipt.Result != "passed" {
		t.Fatal("retained receipt did not verify", err, receipt)
	}
}

func TestRunRejectsBeforeOrAfterReservationWithoutRetrying(t *testing.T) {
	t.Run("existing_output", func(t *testing.T) {
		args, out := approvedInstallArgs(t)
		if err := os.WriteFile(out, []byte("existing"), 0600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		code := run(context.Background(), args, io.Discard, io.Discard, time.Now, func(context.Context, releasepack.ApprovedInstallVerificationOptions) (releasepack.ApprovedInstallVerificationReceipt, error) {
			calls++
			return approvedInstallReceiptFixture(), nil
		})
		if code != 1 || calls != 0 {
			t.Fatal("verification ran without exclusive output reservation", code, calls)
		}
	})
	t.Run("verification_failure", func(t *testing.T) {
		args, out := approvedInstallArgs(t)
		code := run(context.Background(), args, io.Discard, io.Discard, time.Now, func(context.Context, releasepack.ApprovedInstallVerificationOptions) (releasepack.ApprovedInstallVerificationReceipt, error) {
			return releasepack.ApprovedInstallVerificationReceipt{}, errors.New("injected")
		})
		info, err := os.Stat(out)
		if code != 1 || err != nil || info.Size() != 0 {
			t.Fatal("failed execution did not preserve incomplete reservation", code, info, err)
		}
	})
}

func TestRunRejectsInvalidArgumentsAndDependencies(t *testing.T) {
	args, _ := approvedInstallArgs(t)
	for name, tc := range map[string]struct {
		args   []string
		stdout io.Writer
		verify approvedInstallVerifier
		want   int
	}{
		"help":    {args: []string{"--help"}, stdout: io.Discard, verify: successfulApprovedInstallVerifier, want: 0},
		"parse":   {args: []string{"--unknown"}, stdout: io.Discard, verify: successfulApprovedInstallVerifier, want: 2},
		"missing": {args: args[:len(args)-2], stdout: io.Discard, verify: successfulApprovedInstallVerifier, want: 2},
		"extra":   {args: append(args, "extra"), stdout: io.Discard, verify: successfulApprovedInstallVerifier, want: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(context.Background(), tc.args, tc.stdout, io.Discard, time.Now, tc.verify); got != tc.want {
				t.Fatal("unexpected exit", got, tc.want)
			}
		})
	}
	if run(context.Background(), args, nil, io.Discard, time.Now, successfulApprovedInstallVerifier) != 2 ||
		run(context.Background(), args, io.Discard, io.Discard, nil, successfulApprovedInstallVerifier) != 2 ||
		run(context.Background(), args, io.Discard, io.Discard, time.Now, nil) != 2 {
		t.Fatal("nil dependency accepted")
	}
}

func approvedInstallArgs(t *testing.T) ([]string, string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	releaseDir, source := t.TempDir(), t.TempDir()
	installRoot := filepath.Join(t.TempDir(), "new-install")
	out := filepath.Join(t.TempDir(), "approved-install.json")
	return []string{
		"--dir", releaseDir, "--source", source, "--candidate-record", "/evidence/candidate.json",
		"--candidate-record-sha256", digest, "--license-evidence", "/evidence/license.json",
		"--license-evidence-sha256", digest, "--expected-sums-sha256", digest,
		"--trust-record", "/evidence/trust.json", "--trust-record-sha256", digest,
		"--key-id", "release-test-01", "--key-fingerprint", digest,
		"--authorization-record", "/evidence/authorization.json", "--authorization-record-sha256", digest,
		"--target-os", "darwin", "--target-arch", "arm64", "--install-root", installRoot,
		"--verifier-id", "idp:install-verifier", "--host-id", "host:darwin-arm64",
		"--public-key-channel", "https://keys.example.invalid/darwinrouter", "--out", out,
	}, out
}

func successfulApprovedInstallVerifier(context.Context, releasepack.ApprovedInstallVerificationOptions) (releasepack.ApprovedInstallVerificationReceipt, error) {
	return approvedInstallReceiptFixture(), nil
}

func approvedInstallReceiptFixture() releasepack.ApprovedInstallVerificationReceipt {
	digest := "sha256:" + strings.Repeat("a", 64)
	return releasepack.ApprovedInstallVerificationReceipt{
		SchemaVersion: 1, Scope: "darwinrouter-approved-native-install-verification",
		ReleaseVersion: "1.0.0", SourceCommit: strings.Repeat("b", 40), TargetOS: "darwin", TargetArch: "arm64",
		ManifestSHA256: digest, ArtifactName: "DarwinRouter_1.0.0_darwin_arm64.tar.gz", ArtifactSHA256: digest,
		InstalledBinarySHA256: digest, InstalledBinaryMode: "0755", VersionOutput: "darwin 1.0.0",
		ApprovalVerification: releasepack.ApprovedVerificationResult{
			CandidateRecordSHA256: digest, LicenseEvidenceSHA256: digest, SHA256SUMSSHA256: digest,
			TrustRecordSHA256: digest, AuthorizationRecordSHA256: digest, KeyID: "release-test-01",
			KeyFingerprint: digest, SignatureFileSHA256: digest,
		},
		VerifierID: "idp:install-verifier", HostID: "host:darwin-arm64",
		PublicKeyChannel: "https://keys.example.invalid/darwinrouter", VerifiedAt: "2026-09-14T18:34:56Z", Result: "passed",
	}
}
