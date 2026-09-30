package releasepack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/githubverify"
)

type PublishedReleaseReader interface {
	Verify(context.Context, githubverify.Plan) (githubverify.Observation, error)
}

type PublishedVerificationOptions struct {
	Preflight   PublicationPreflightOptions
	DownloadDir string
	VerifierID  string
}

// PostPublicationVerificationPolicy identifies the exact verifier contract
// enforced by this implementation. It is fixed by code rather than supplied by
// an operator so a receipt cannot claim a different policy than the one run.
const PostPublicationVerificationPolicy = "nexusrouter-github-post-publication-verification/v2"

// PostPublicationReceipt is canonical public evidence of one remote observation
// and approval-bound verification. It is not a durability or future-state claim.
type PostPublicationReceipt struct {
	SchemaVersion                  int                        `json:"schema_version"`
	Scope                          string                     `json:"scope"`
	PublicationAuthorizationSHA256 string                     `json:"publication_authorization_sha256"`
	Repository                     string                     `json:"repository"`
	ReleaseVersion                 string                     `json:"release_version"`
	ReleaseID                      int64                      `json:"release_id"`
	ReleaseURL                     string                     `json:"release_url"`
	Tag                            string                     `json:"tag"`
	SourceCommit                   string                     `json:"source_commit"`
	TagObjectSHA                   string                     `json:"tag_object_sha"`
	TagMessage                     string                     `json:"tag_message"`
	Tagger                         PublicationTagger          `json:"tagger"`
	ReleaseTitle                   string                     `json:"release_title"`
	ReleaseNotesSHA256             string                     `json:"release_notes_sha256"`
	Prerelease                     bool                       `json:"prerelease"`
	AuthorizedMakeLatest           bool                       `json:"authorized_make_latest"`
	Immutable                      bool                       `json:"immutable"`
	PublishedAt                    string                     `json:"published_at"`
	ObservedAt                     string                     `json:"observed_at"`
	VerifierID                     string                     `json:"verifier_id"`
	VerificationPolicy             string                     `json:"verification_policy"`
	ReleaseAttestation             PostPublicationAttestation `json:"release_attestation"`
	Assets                         []PostPublicationAsset     `json:"assets"`
	ApprovalVerification           ApprovedVerificationResult `json:"approval_verification"`
}

// PostPublicationAttestation binds the independently checked GitHub release
// attestation and the exact local gh verifier used to check it.
type PostPublicationAttestation struct {
	VerifierVersion      string `json:"verifier_version"`
	VerifierBinarySHA256 string `json:"verifier_binary_sha256"`
	VerifiedResultSHA256 string `json:"verified_result_sha256"`
	BundleSHA256         string `json:"bundle_sha256"`
	Signer               string `json:"signer"`
	Issuer               string `json:"issuer"`
	PredicateType        string `json:"predicate_type"`
	TimestampCount       int    `json:"timestamp_count"`
	TagSubjectDigest     string `json:"tag_subject_digest"`
}

type PostPublicationAsset struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	ContentType  string `json:"content_type"`
	ServerSHA256 string `json:"server_sha256"`
	LocalSHA256  string `json:"local_sha256"`
	DownloadURL  string `json:"download_url"`
}

// VerifyPublishedRelease performs read-only GitHub observation, writes only a
// fresh local download directory, and reuses the production approval verifier.
func VerifyPublishedRelease(ctx context.Context, remote PublishedReleaseReader, options PublishedVerificationOptions) (PostPublicationReceipt, error) {
	return verifyPublishedReleaseWithLicenseEvidenceVerifier(ctx, remote, options, verifyLicenseEvidenceRecord)
}

func verifyPublishedReleaseWithLicenseEvidenceVerifier(ctx context.Context, remote PublishedReleaseReader, options PublishedVerificationOptions, verifyEvidence licenseEvidenceRecordVerifier) (PostPublicationReceipt, error) {
	var empty PostPublicationReceipt
	if ctx == nil || remote == nil || options.DownloadDir == "" || !ValidPostPublicationVerifierID(options.VerifierID) || verifyEvidence == nil {
		return empty, ErrPublicationAuthorization
	}
	preflight, err := verifyPublicationPreflightWithLicenseEvidenceVerifier(ctx, options.Preflight, verifyEvidence)
	if err != nil || !ValidPostPublicationVerifierID(preflight.PublicationApproverID) || options.VerifierID == preflight.PublicationApproverID {
		return empty, ErrPublicationAuthorization
	}
	body, err := readPublicationInput(options.Preflight.ReleaseNotesFile, maxReleaseNotes)
	if err != nil || publicationDigest(body) != preflight.ReleaseNotesSHA256 {
		return empty, ErrPublicationAuthorization
	}
	expectedAssets := make([]githubverify.ExpectedAsset, len(preflight.Assets))
	for i, asset := range preflight.Assets {
		expectedAssets[i] = githubverify.ExpectedAsset{
			Name: asset.Name, Size: asset.Size, SHA256: asset.SHA256, ContentType: publicationContentType(asset.Name),
		}
	}
	observation, err := remote.Verify(ctx, githubverify.Plan{
		Repository: preflight.Repository, Tag: preflight.Tag, Commit: preflight.SourceCommit,
		TagMessage: preflight.TagMessage,
		Tagger:     githubverify.Tagger{Name: preflight.Tagger.Name, Email: preflight.Tagger.Email, Date: preflight.Tagger.Date},
		Title:      preflight.ReleaseTitle, Body: body, Prerelease: preflight.Prerelease,
		Assets: expectedAssets, DownloadDir: options.DownloadDir,
		ForbiddenRoots: []string{options.Preflight.Verification.Source, options.Preflight.Verification.Dir},
	})
	attestation := postPublicationAttestation(observation.ReleaseAttestation)
	if err != nil || !observation.Immutable || observation.Repository != preflight.Repository ||
		observation.Tag != preflight.Tag || observation.Commit != preflight.SourceCommit ||
		!commitPattern.MatchString(observation.TagObjectSHA) || observation.TagMessage != preflight.TagMessage ||
		observation.Tagger != (githubverify.Tagger{Name: preflight.Tagger.Name, Email: preflight.Tagger.Email, Date: preflight.Tagger.Date}) ||
		observation.Title != preflight.ReleaseTitle || observation.BodySHA256 != preflight.ReleaseNotesSHA256 ||
		!validPostPublicationAttestation(attestation, observation.TagObjectSHA) ||
		observation.Prerelease != preflight.Prerelease || len(observation.Assets) != len(preflight.Assets) {
		return empty, ErrPublicationAuthorization
	}
	for i, asset := range observation.Assets {
		if asset.Name != preflight.Assets[i].Name || asset.Size != preflight.Assets[i].Size ||
			asset.ContentType != publicationContentType(asset.Name) || asset.ServerSHA256 != preflight.Assets[i].SHA256 ||
			asset.LocalSHA256 != preflight.Assets[i].SHA256 {
			return empty, ErrPublicationAuthorization
		}
	}
	verificationOptions := options.Preflight.Verification
	verificationOptions.Dir = options.DownloadDir
	verification, err := verifyApprovedWithLicenseEvidenceVerifier(ctx, verificationOptions, verifyEvidence)
	if err != nil || verification.CandidateRecordSHA256 != verificationOptions.ExpectedCandidateSHA256 ||
		verification.LicenseEvidenceSHA256 != verificationOptions.ExpectedLicenseEvidenceSHA256 ||
		verification.SHA256SUMSSHA256 != verificationOptions.ExpectedSumsSHA256 ||
		verification.TrustRecordSHA256 != verificationOptions.ExpectedTrustRecordSHA256 ||
		verification.AuthorizationRecordSHA256 != verificationOptions.ExpectedAuthorizationSHA256 ||
		verification.SignatureFileSHA256 != publicationAssetSHA(preflight.Assets, signatureName) ||
		verification.KeyID != verificationOptions.ExpectedKeyID || verification.KeyFingerprint != verificationOptions.ExpectedKeyFingerprint {
		return empty, ErrPublicationAuthorization
	}
	receipt := PostPublicationReceipt{
		SchemaVersion: 2, Scope: "nexusrouter-github-post-publication-verification",
		PublicationAuthorizationSHA256: preflight.PublicationAuthorizationSHA256,
		Repository:                     preflight.Repository, ReleaseVersion: preflight.ReleaseVersion,
		ReleaseID: observation.ReleaseID, ReleaseURL: observation.ReleaseURL,
		Tag: preflight.Tag, SourceCommit: preflight.SourceCommit, ReleaseTitle: preflight.ReleaseTitle,
		TagObjectSHA: observation.TagObjectSHA, TagMessage: observation.TagMessage, Tagger: preflight.Tagger,
		ReleaseNotesSHA256: preflight.ReleaseNotesSHA256, Prerelease: preflight.Prerelease,
		AuthorizedMakeLatest: preflight.MakeLatest, Immutable: true,
		PublishedAt: observation.PublishedAt, ObservedAt: observation.ObservedAt,
		VerifierID: options.VerifierID, VerificationPolicy: PostPublicationVerificationPolicy,
		ReleaseAttestation: attestation, ApprovalVerification: verification,
	}
	for _, asset := range observation.Assets {
		receipt.Assets = append(receipt.Assets, PostPublicationAsset{
			ID: asset.ID, Name: asset.Name, Size: asset.Size, ContentType: asset.ContentType, ServerSHA256: asset.ServerSHA256,
			LocalSHA256: asset.LocalSHA256, DownloadURL: asset.DownloadURL,
		})
	}
	if _, err = MarshalPostPublicationReceipt(receipt); err != nil {
		return empty, ErrPublicationAuthorization
	}
	return receipt, nil
}

func postPublicationAttestation(attestation githubverify.ReleaseAttestationEvidence) PostPublicationAttestation {
	return PostPublicationAttestation{
		VerifierVersion: attestation.VerifierVersion, VerifierBinarySHA256: attestation.VerifierBinarySHA256,
		VerifiedResultSHA256: attestation.VerifiedResultSHA256, BundleSHA256: attestation.BundleSHA256,
		Signer: attestation.Signer, Issuer: attestation.Issuer, PredicateType: attestation.PredicateType,
		TimestampCount: attestation.TimestampCount, TagSubjectDigest: attestation.TagSubjectDigest,
	}
}

func MarshalPostPublicationReceipt(receipt PostPublicationReceipt) ([]byte, error) {
	if receipt.SchemaVersion != 2 || receipt.Scope != "nexusrouter-github-post-publication-verification" ||
		!trustFingerprint(receipt.PublicationAuthorizationSHA256) || !githubRepository.MatchString(receipt.Repository) ||
		receipt.ReleaseID < 1 || validate(Options{Version: receipt.ReleaseVersion, Commit: receipt.SourceCommit, Out: "release"}) != nil ||
		receipt.Tag != "v"+receipt.ReleaseVersion || !validGitHubReleaseURL(receipt.ReleaseURL, receipt.Repository, receipt.Tag) ||
		!commitPattern.MatchString(receipt.TagObjectSHA) || receipt.TagMessage != "NexusRouter release "+receipt.Tag || !validPublicationTagger(receipt.Tagger) ||
		receipt.ReleaseTitle != "NexusRouter "+receipt.Tag || !trustFingerprint(receipt.ReleaseNotesSHA256) || !receipt.Immutable ||
		(receipt.Prerelease && receipt.AuthorizedMakeLatest) || receipt.Prerelease != strings.Contains(receipt.ReleaseVersion, "-") ||
		!wholeSecondUTC(receipt.PublishedAt) || !wholeSecondUTC(receipt.ObservedAt) || len(receipt.Assets) != 7 ||
		!observationAfterPublication(receipt.PublishedAt, receipt.ObservedAt) || !ValidPostPublicationVerifierID(receipt.VerifierID) ||
		receipt.VerificationPolicy != PostPublicationVerificationPolicy ||
		!validPostPublicationAttestation(receipt.ReleaseAttestation, receipt.TagObjectSHA) ||
		!validReceiptVerification(receipt.ApprovalVerification) {
		return nil, ErrPublicationAuthorization
	}
	previous := ""
	ids := map[int64]bool{}
	names := publicationAssetNames(receipt.ReleaseVersion)
	for i, asset := range receipt.Assets {
		if asset.ID < 1 || ids[asset.ID] || !signingBasename(asset.Name) || asset.Name <= previous || asset.Size < 1 ||
			asset.Name != names[i] || asset.ContentType != publicationContentType(asset.Name) ||
			!trustFingerprint(asset.ServerSHA256) || asset.LocalSHA256 != asset.ServerSHA256 ||
			!validGitHubAssetURL(asset.DownloadURL, receipt.Repository, receipt.Tag, asset.Name) {
			return nil, ErrPublicationAuthorization
		}
		ids[asset.ID], previous = true, asset.Name
	}
	if receipt.ApprovalVerification.SHA256SUMSSHA256 != receiptAssetSHA(receipt.Assets, "SHA256SUMS") ||
		receipt.ApprovalVerification.SignatureFileSHA256 != receiptAssetSHA(receipt.Assets, signatureName) {
		return nil, ErrPublicationAuthorization
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, ErrPublicationAuthorization
	}
	return append(body, '\n'), nil
}

func validPostPublicationAttestation(attestation PostPublicationAttestation, tagObjectSHA string) bool {
	return len(attestation.VerifierVersion) <= 100 && semver.MatchString(attestation.VerifierVersion) &&
		trustFingerprint(attestation.VerifierBinarySHA256) && trustFingerprint(attestation.VerifiedResultSHA256) &&
		trustFingerprint(attestation.BundleSHA256) && attestation.Signer == githubverify.ReleaseAttestationSigner &&
		attestation.Issuer == githubverify.ReleaseAttestationIssuer && attestation.PredicateType == githubverify.ReleaseAttestationPredicateType &&
		attestation.TimestampCount >= 1 && attestation.TimestampCount <= 1_000_000 &&
		commitPattern.MatchString(tagObjectSHA) && attestation.TagSubjectDigest == "sha1:"+tagObjectSHA
}

// ValidPostPublicationVerifierID reports whether value is a bounded, canonical
// operator identity suitable for durable independent-verification evidence.
func ValidPostPublicationVerifierID(value string) bool {
	return rollbackIdentity.MatchString(value)
}

func receiptAssetSHA(assets []PostPublicationAsset, name string) string {
	for _, asset := range assets {
		if asset.Name == name {
			return asset.ServerSHA256
		}
	}
	return ""
}

func publicationContentType(name string) string {
	if strings.HasSuffix(name, ".tar.gz") {
		return "application/gzip"
	}
	return "application/octet-stream"
}

func publicationAssetSHA(assets []PublicationAsset, name string) string {
	for _, asset := range assets {
		if asset.Name == name {
			return asset.SHA256
		}
	}
	return ""
}

func wholeSecondUTC(value string) bool {
	when, err := time.Parse("2006-01-02T15:04:05Z", value)
	return err == nil && when.Format("2006-01-02T15:04:05Z") == value
}

func observationAfterPublication(published, observed string) bool {
	publishedAt, publishedErr := time.Parse("2006-01-02T15:04:05Z", published)
	observedAt, observedErr := time.Parse("2006-01-02T15:04:05Z", observed)
	return publishedErr == nil && observedErr == nil && !observedAt.Before(publishedAt)
}

func validGitHubReleaseURL(raw, repository, tag string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "github.com") && u.Port() == "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.EscapedPath() == "/"+repository+"/releases/tag/"+url.PathEscape(tag)
}

func validGitHubAssetURL(raw, repository, tag, name string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "github.com") && u.Port() == "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		u.EscapedPath() == "/"+repository+"/releases/download/"+url.PathEscape(tag)+"/"+url.PathEscape(name)
}

func validReceiptVerification(v ApprovedVerificationResult) bool {
	return trustFingerprint(v.CandidateRecordSHA256) && trustFingerprint(v.LicenseEvidenceSHA256) &&
		trustFingerprint(v.SHA256SUMSSHA256) && trustFingerprint(v.TrustRecordSHA256) &&
		trustFingerprint(v.AuthorizationRecordSHA256) && trustKeyID.MatchString(v.KeyID) &&
		trustFingerprint(v.KeyFingerprint) && trustFingerprint(v.SignatureFileSHA256)
}

func ParsePostPublicationReceipt(body []byte) (PostPublicationReceipt, error) {
	var receipt PostPublicationReceipt
	if len(body) == 0 || len(body) > 64<<10 || json.Unmarshal(body, &receipt) != nil {
		return PostPublicationReceipt{}, ErrPublicationAuthorization
	}
	canonical, err := MarshalPostPublicationReceipt(receipt)
	if err != nil || !bytes.Equal(body, canonical) {
		return PostPublicationReceipt{}, ErrPublicationAuthorization
	}
	return receipt, nil
}
