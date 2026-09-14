package releasepack

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApprovedVerificationRoundTrip(t *testing.T) {
	signing, _ := approvedSigningIntegrationFixture(t)
	if err := SignApproved(context.Background(), signing); err != nil {
		t.Fatal(err)
	}
	options := verificationOptions(signing)
	result, err := VerifyApproved(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile(filepath.Join(signing.Dir, signatureName))
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateRecordSHA256 != signing.ExpectedCandidateSHA256 ||
		result.LicenseEvidenceSHA256 != signing.ExpectedLicenseEvidenceSHA256 ||
		result.SHA256SUMSSHA256 != signing.ExpectedSumsSHA256 ||
		result.TrustRecordSHA256 != signing.ExpectedTrustRecordSHA256 ||
		result.AuthorizationRecordSHA256 != signing.ExpectedAuthorizationSHA256 ||
		result.KeyID != signing.ExpectedKeyID || result.KeyFingerprint != signing.ExpectedKeyFingerprint ||
		result.SignatureFileSHA256 != prefixedDigest(signature) {
		t.Fatal("incorrect verification evidence", result)
	}
}

func TestApprovedVerificationRejectsUnboundOrInvalidInputs(t *testing.T) {
	for _, scenario := range []string{"candidate", "license_evidence", "license_evidence_missing", "license_evidence_swapped", "sums", "trust", "authorization", "key_id", "fingerprint", "policy", "unsigned", "artifact", "signature", "dirty_source", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			signing, _ := approvedSigningFastFixture(t)
			if scenario != "unsigned" {
				if err := signApprovedFixture(context.Background(), signing, func(path string) ([]byte, error) { return signingKeyFile(path, true) }); err != nil {
					t.Fatal(err)
				}
			}
			options := verificationOptions(signing)
			ctx := context.Background()
			switch scenario {
			case "candidate":
				options.ExpectedCandidateSHA256 = invalidPublicDigest()
			case "license_evidence":
				options.ExpectedLicenseEvidenceSHA256 = invalidPublicDigest()
			case "license_evidence_missing":
				options.LicenseEvidenceFile = filepath.Join(t.TempDir(), "missing-license-evidence.json")
			case "license_evidence_swapped":
				options.LicenseEvidenceFile = options.CandidateRecordFile
				options.ExpectedLicenseEvidenceSHA256 = options.ExpectedCandidateSHA256
			case "sums":
				options.ExpectedSumsSHA256 = invalidPublicDigest()
			case "trust":
				options.ExpectedTrustRecordSHA256 = invalidPublicDigest()
			case "authorization":
				options.ExpectedAuthorizationSHA256 = invalidPublicDigest()
			case "key_id":
				options.ExpectedKeyID = "other-key"
			case "fingerprint":
				options.ExpectedKeyFingerprint = invalidPublicDigest()
			case "policy":
				_, authorization := readAuthorizationFixture(t, signing.AuthorizationRecordFile)
				authorization.ReleasePolicyURL = "https://example.invalid/other-policy"
				body := canonicalAuthorizationFixture(t, authorization)
				if err := os.WriteFile(signing.AuthorizationRecordFile, body, 0644); err != nil {
					t.Fatal(err)
				}
				options.ExpectedAuthorizationSHA256 = prefixedDigest(body)
			case "artifact":
				if err := os.WriteFile(filepath.Join(signing.Dir, "manifest.json"), []byte("{}\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "signature":
				path := filepath.Join(signing.Dir, signatureName)
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				body[0] ^= 1
				if err = os.WriteFile(path, body, 0644); err != nil {
					t.Fatal(err)
				}
			case "dirty_source":
				if err := os.WriteFile(filepath.Join(signing.Source, "dirty"), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if result, err := verifyApprovedFixture(ctx, options); err == nil || result != (ApprovedVerificationResult{}) {
				t.Fatal("unsafe release verified", result, err)
			}
		})
	}
}

func TestApprovedVerificationRejectsSignedArtifactNoticeOutsideLicenseEvidence(t *testing.T) {
	signing, _ := approvedSigningFixtureWithExecutableVersionAndEvidence(t, true, "", "", false)
	if err := signUncheckedForTest(signing.Dir, signing.KeyFile); err != nil {
		t.Fatal(err)
	}
	if result, err := verifyApprovedFixture(context.Background(), verificationOptions(signing)); err == nil || result != (ApprovedVerificationResult{}) {
		t.Fatal("signed artifact notice outside license evidence verified", result, err)
	}
}

func verifyApprovedFixture(ctx context.Context, options ApprovedVerificationOptions, protectedPaths ...string) (ApprovedVerificationResult, error) {
	wantProtected := append([]string{options.Dir}, protectedPaths...)
	return verifyApprovedWithLicenseEvidenceVerifier(ctx, options, fixtureLicenseEvidenceVerifier(wantProtected...), protectedPaths...)
}

func verificationOptions(signing ApprovedSigningOptions) ApprovedVerificationOptions {
	return ApprovedVerificationOptions{
		Dir: signing.Dir, CandidateRecordFile: signing.CandidateRecordFile,
		ExpectedCandidateSHA256: signing.ExpectedCandidateSHA256, Source: signing.Source,
		LicenseEvidenceFile:           signing.LicenseEvidenceFile,
		ExpectedLicenseEvidenceSHA256: signing.ExpectedLicenseEvidenceSHA256,
		ExpectedSumsSHA256:            signing.ExpectedSumsSHA256, TrustRecordFile: signing.TrustRecordFile,
		ExpectedTrustRecordSHA256: signing.ExpectedTrustRecordSHA256, ExpectedKeyID: signing.ExpectedKeyID,
		ExpectedKeyFingerprint: signing.ExpectedKeyFingerprint, AuthorizationRecordFile: signing.AuthorizationRecordFile,
		ExpectedAuthorizationSHA256: signing.ExpectedAuthorizationSHA256,
	}
}

func invalidPublicDigest() string {
	return "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
}
