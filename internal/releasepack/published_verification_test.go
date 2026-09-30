package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/githubverify"
)

func TestVerifyPublishedReleaseReverifiesFreshRemoteBytes(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	t.Run("valid", func(t *testing.T) {
		download := filepath.Join(t.TempDir(), "download")
		reader := &fixtureReleaseReader{source: signedDir}
		receipt, err := VerifyPublishedRelease(context.Background(), reader, PublishedVerificationOptions{Preflight: preflight, DownloadDir: download, VerifierID: "idp:release-verifier"})
		if err != nil || receipt.Repository != preflight.ExpectedRepository || !receipt.Immutable || len(receipt.Assets) != 7 {
			t.Fatal("published release rejected", receipt, err)
		}
		wantAttestation := PostPublicationAttestation{
			VerifierVersion: "2.98.0", VerifierBinarySHA256: testInstallDigest("8"),
			VerifiedResultSHA256: testInstallDigest("9"), BundleSHA256: testInstallDigest("a"),
			Signer: githubverify.ReleaseAttestationSigner, Issuer: githubverify.ReleaseAttestationIssuer,
			PredicateType: githubverify.ReleaseAttestationPredicateType, TimestampCount: 1,
			TagSubjectDigest: "sha1:" + receipt.TagObjectSHA,
		}
		if receipt.SchemaVersion != 2 || receipt.ReleaseAttestation != wantAttestation {
			t.Fatal("release attestation was not bound", receipt.ReleaseAttestation)
		}
		body, err := MarshalPostPublicationReceipt(receipt)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParsePostPublicationReceipt(body)
		if err != nil || parsed.PublicationAuthorizationSHA256 != preflight.ExpectedPublicationAuthorizationSHA256 ||
			parsed.VerifierID != "idp:release-verifier" || parsed.VerificationPolicy != PostPublicationVerificationPolicy ||
			parsed.ReleaseAttestation != wantAttestation {
			t.Fatal("canonical receipt rejected", parsed, err)
		}
	})
	t.Run("replaced_download", func(t *testing.T) {
		reader := &fixtureReleaseReader{source: signedDir, corruptAfterCopy: true}
		result, err := VerifyPublishedRelease(context.Background(), reader, PublishedVerificationOptions{
			Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"), VerifierID: "idp:release-verifier",
		})
		if err == nil || result.SchemaVersion != 0 || result.Repository != "" || len(result.Assets) != 0 {
			t.Fatal("replaced remote bytes accepted", result, err)
		}
	})
}

func TestVerifyPublishedReleaseRejectsMissingMalformedOrMismatchedAttestation(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	for name, mutate := range map[string]func(*githubverify.ReleaseAttestationEvidence){
		"missing":          func(a *githubverify.ReleaseAttestationEvidence) { *a = githubverify.ReleaseAttestationEvidence{} },
		"verifier_version": func(a *githubverify.ReleaseAttestationEvidence) { a.VerifierVersion = "gh current" },
		"verifier_binary":  func(a *githubverify.ReleaseAttestationEvidence) { a.VerifierBinarySHA256 = "bad" },
		"verified_result":  func(a *githubverify.ReleaseAttestationEvidence) { a.VerifiedResultSHA256 = "bad" },
		"bundle":           func(a *githubverify.ReleaseAttestationEvidence) { a.BundleSHA256 = "bad" },
		"signer":           func(a *githubverify.ReleaseAttestationEvidence) { a.Signer = "https://evil.example" },
		"issuer":           func(a *githubverify.ReleaseAttestationEvidence) { a.Issuer = "invalid\nissuer" },
		"predicate": func(a *githubverify.ReleaseAttestationEvidence) {
			a.PredicateType = "https://example.invalid/predicate"
		},
		"timestamp_count": func(a *githubverify.ReleaseAttestationEvidence) { a.TimestampCount = 0 },
		"tag_subject": func(a *githubverify.ReleaseAttestationEvidence) {
			a.TagSubjectDigest = "sha1:" + strings.Repeat("d", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &fixtureReleaseReader{source: signedDir, mutateAttestation: mutate}
			result, err := VerifyPublishedRelease(context.Background(), reader, PublishedVerificationOptions{
				Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"), VerifierID: "idp:release-verifier",
			})
			if err == nil || result.SchemaVersion != 0 {
				t.Fatal("invalid release attestation accepted", result.ReleaseAttestation, err)
			}
		})
	}
}

func TestPostPublicationReceiptRejectsNonGitHubEvidenceURL(t *testing.T) {
	if validGitHubReleaseURL("https://evil.example/acme/router/releases/tag/v1.0.0", "acme/router", "v1.0.0") {
		t.Fatal("non-GitHub receipt URL accepted")
	}
}

func TestPostPublicationReceiptRejectsCrossAuthorityAssetURL(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	receipt, err := VerifyPublishedRelease(context.Background(), &fixtureReleaseReader{source: signedDir}, PublishedVerificationOptions{
		Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"), VerifierID: "idp:release-verifier",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Assets[0].DownloadURL = "https://github.com/other/repository/releases/download/" + receipt.Tag + "/" + receipt.Assets[0].Name
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	if _, err = ParsePostPublicationReceipt(body); err == nil {
		t.Fatal("cross-repository asset URL accepted by canonical receipt parser")
	}
}

func TestPostPublicationReceiptRejectsMissingOrTamperedVerificationIdentity(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	receipt, err := VerifyPublishedRelease(context.Background(), &fixtureReleaseReader{source: signedDir}, PublishedVerificationOptions{
		Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"), VerifierID: "idp:release-verifier",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PostPublicationReceipt){
		"schema": func(r *PostPublicationReceipt) { r.SchemaVersion = 1 },
		"missing_attestation": func(r *PostPublicationReceipt) {
			r.ReleaseAttestation = PostPublicationAttestation{}
		},
		"missing_verifier": func(r *PostPublicationReceipt) { r.VerifierID = "" },
		"bad_verifier":     func(r *PostPublicationReceipt) { r.VerifierID = "INVALID VERIFIER" },
		"missing_policy":   func(r *PostPublicationReceipt) { r.VerificationPolicy = "" },
		"changed_policy": func(r *PostPublicationReceipt) {
			r.VerificationPolicy = "nexusrouter-github-post-publication-verification/v1"
		},
		"attestation_version": func(r *PostPublicationReceipt) { r.ReleaseAttestation.VerifierVersion = "gh current" },
		"attestation_result":  func(r *PostPublicationReceipt) { r.ReleaseAttestation.VerifiedResultSHA256 = "bad" },
		"attestation_binary":  func(r *PostPublicationReceipt) { r.ReleaseAttestation.VerifierBinarySHA256 = "bad" },
		"attestation_bundle":  func(r *PostPublicationReceipt) { r.ReleaseAttestation.BundleSHA256 = "bad" },
		"attestation_signer":  func(r *PostPublicationReceipt) { r.ReleaseAttestation.Signer = "https://evil.example" },
		"attestation_issuer":  func(r *PostPublicationReceipt) { r.ReleaseAttestation.Issuer = "bad\nissuer" },
		"attestation_predicate": func(r *PostPublicationReceipt) {
			r.ReleaseAttestation.PredicateType = "https://example.invalid/predicate"
		},
		"attestation_timestamp": func(r *PostPublicationReceipt) { r.ReleaseAttestation.TimestampCount = 0 },
		"attestation_subject": func(r *PostPublicationReceipt) {
			r.ReleaseAttestation.TagSubjectDigest = "sha1:" + strings.Repeat("e", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			change(&changed)
			body, marshalErr := json.MarshalIndent(changed, "", "  ")
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, parseErr := ParsePostPublicationReceipt(append(body, '\n')); parseErr == nil {
				t.Fatal("tampered verification identity accepted")
			}
		})
	}
}

func TestVerifyPublishedReleaseRejectsPublicationApproverAsVerifierBeforeRemote(t *testing.T) {
	preflight, signedDir := publishedFixture(t)
	reader := &fixtureReleaseReader{source: signedDir}
	result, err := VerifyPublishedRelease(context.Background(), reader, PublishedVerificationOptions{
		Preflight: preflight, DownloadDir: filepath.Join(t.TempDir(), "download"), VerifierID: "idp:publication-approver",
	})
	if err == nil || reader.called || result.SchemaVersion != 0 {
		t.Fatal("publication approver accepted as independent verifier", result, reader.called, err)
	}
}

func publishedFixture(t *testing.T) (PublicationPreflightOptions, string) {
	return publishedFixtureWithExecutable(t, false)
}

func publishedExecutableFixture(t *testing.T) (PublicationPreflightOptions, string) {
	return publishedFixtureWithExecutable(t, true)
}

func publishedControlledExecutableFixture(t *testing.T, policy goReconstructionPolicyOptions) (PublicationPreflightOptions, string, licenseEvidenceRecordVerifier) {
	signing, _ := approvedSigningControlledExecutableFixture(t, policy)
	verifier := func(ctx context.Context, recordPath, expectedSHA256, source string, protectedPaths ...string) (LicenseEvidence, error) {
		return verifyLicenseEvidenceRecordWithPolicy(ctx, recordPath, expectedSHA256, source, policy, protectedPaths...)
	}
	preflight, signedDir := publishedFixtureFromSigningWithVerifier(t, signing, verifier)
	return preflight, signedDir, verifier
}

func publishedFixtureWithExecutable(t *testing.T, executableNative bool) (PublicationPreflightOptions, string) {
	t.Helper()
	var signing ApprovedSigningOptions
	if executableNative {
		signing, _ = approvedSigningExecutableFixture(t)
	} else {
		signing, _ = approvedSigningFixture(t)
	}
	return publishedFixtureFromSigning(t, signing)
}

func publishedFixtureFromSigning(t *testing.T, signing ApprovedSigningOptions) (PublicationPreflightOptions, string) {
	return publishedFixtureFromSigningWithVerifier(t, signing, nil)
}

func publishedFixtureFromSigningWithVerifier(t *testing.T, signing ApprovedSigningOptions, verifyEvidence licenseEvidenceRecordVerifier) (PublicationPreflightOptions, string) {
	t.Helper()
	var err error
	if verifyEvidence == nil {
		err = SignApproved(context.Background(), signing)
	} else {
		err = signApprovedWithLicenseEvidenceVerifier(context.Background(), signing, func(path string) ([]byte, error) {
			return signingKeyFile(path, true)
		}, verifyEvidence)
	}
	if err != nil {
		t.Fatal("sign published fixture", err)
	}
	verification := verificationOptions(signing)
	var verified ApprovedVerificationResult
	if verifyEvidence == nil {
		verified, err = VerifyApproved(context.Background(), verification)
	} else {
		verified, err = verifyApprovedWithLicenseEvidenceVerifier(context.Background(), verification, verifyEvidence)
	}
	if err != nil {
		t.Fatal("verify published fixture", err)
	}
	_, candidate, err := readCandidateRecord(signing.CandidateRecordFile)
	if err != nil {
		t.Fatal(err)
	}
	notesFile := candidateFinalNotesFixture(t, candidate, signing.Source)
	notes, err := os.ReadFile(notesFile)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := publicationAssets(signing.Dir)
	if err != nil {
		t.Fatal(err)
	}
	record := publicationAuthorizationFixture()
	record.ReleaseVersion, record.SourceCommit = candidate.ReleaseVersion, candidate.SourceCommit
	record.Tag, record.ReleaseTitle = "v"+candidate.ReleaseVersion, "NexusRouter v"+candidate.ReleaseVersion
	record.ReleaseNotesSHA256, record.Assets = publicationDigest(notes), assets
	record.CandidateRecordSHA256, record.LicenseEvidenceSHA256 = verified.CandidateRecordSHA256, verified.LicenseEvidenceSHA256
	record.SHA256SUMSSHA256, record.SignatureFileSHA256 = verified.SHA256SUMSSHA256, verified.SignatureFileSHA256
	record.TrustRecordSHA256, record.SigningAuthorizationSHA256 = verified.TrustRecordSHA256, verified.AuthorizationRecordSHA256
	body := canonicalPublicationAuthorization(t, record)
	recordFile := filepath.Join(t.TempDir(), "publication.json")
	if err = os.WriteFile(recordFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	return PublicationPreflightOptions{
		Verification: verification, PublicationAuthorizationFile: recordFile,
		ExpectedPublicationAuthorizationSHA256: publicationDigest(body),
		ExpectedRepository:                     record.Repository, ReleaseNotesFile: notesFile,
	}, signing.Dir
}

type fixtureReleaseReader struct {
	source            string
	corruptAfterCopy  bool
	called            bool
	mutateAttestation func(*githubverify.ReleaseAttestationEvidence)
}

func (f *fixtureReleaseReader) Verify(_ context.Context, plan githubverify.Plan) (githubverify.Observation, error) {
	f.called = true
	if err := os.Mkdir(plan.DownloadDir, 0700); err != nil {
		return githubverify.Observation{}, err
	}
	assets := make([]githubverify.ObservedAsset, 0, len(plan.Assets))
	for i, asset := range plan.Assets {
		body, err := os.ReadFile(filepath.Join(f.source, asset.Name))
		if err != nil || int64(len(body)) != asset.Size {
			return githubverify.Observation{}, ErrPublicationAuthorization
		}
		if err = os.WriteFile(filepath.Join(plan.DownloadDir, asset.Name), body, 0644); err != nil {
			return githubverify.Observation{}, err
		}
		assets = append(assets, githubverify.ObservedAsset{
			ID: int64(i + 1), Name: asset.Name, Size: asset.Size, ContentType: publicationContentType(asset.Name), ServerSHA256: asset.SHA256,
			LocalSHA256: asset.SHA256, DownloadURL: "https://github.com/" + plan.Repository + "/releases/download/" + plan.Tag + "/" + asset.Name,
		})
	}
	if f.corruptAfterCopy {
		if err := os.WriteFile(filepath.Join(plan.DownloadDir, signatureName), []byte("replaced\n"), 0644); err != nil {
			return githubverify.Observation{}, err
		}
	}
	attestation := githubverify.ReleaseAttestationEvidence{
		VerifierVersion: "2.98.0", VerifierBinarySHA256: testInstallDigest("8"),
		VerifiedResultSHA256: testInstallDigest("9"), BundleSHA256: testInstallDigest("a"),
		Signer: githubverify.ReleaseAttestationSigner, Issuer: githubverify.ReleaseAttestationIssuer,
		PredicateType: githubverify.ReleaseAttestationPredicateType, TimestampCount: 1,
		TagSubjectDigest: "sha1:" + strings.Repeat("c", 40),
	}
	if f.mutateAttestation != nil {
		f.mutateAttestation(&attestation)
	}
	return githubverify.Observation{
		Repository: plan.Repository, ReleaseID: 7, ReleaseURL: "https://github.com/" + plan.Repository + "/releases/tag/" + plan.Tag,
		Tag: plan.Tag, Commit: plan.Commit, TagObjectSHA: strings.Repeat("c", 40), TagMessage: plan.TagMessage, Tagger: plan.Tagger,
		Title: plan.Title, BodySHA256: publicationDigest(plan.Body),
		Prerelease: plan.Prerelease, Immutable: true, PublishedAt: "2026-09-07T01:00:00Z",
		ObservedAt: "2026-09-07T01:01:00Z", ReleaseAttestation: attestation, Assets: assets, DownloadDir: plan.DownloadDir,
	}, nil
}
