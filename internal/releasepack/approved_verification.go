package releasepack

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"path/filepath"
)

// ApprovedVerificationOptions binds independent verification to the exact
// public inputs used for an authorized production signing operation. License
// evidence remains mechanical evidence and does not itself grant approval.
type ApprovedVerificationOptions struct {
	Dir                           string
	CandidateRecordFile           string
	ExpectedCandidateSHA256       string
	LicenseEvidenceFile           string
	ExpectedLicenseEvidenceSHA256 string
	Source                        string
	ExpectedSumsSHA256            string
	TrustRecordFile               string
	ExpectedTrustRecordSHA256     string
	ExpectedKeyID                 string
	ExpectedKeyFingerprint        string
	AuthorizationRecordFile       string
	ExpectedAuthorizationSHA256   string
}

// ApprovedVerificationResult contains public exact-byte identities only. It is
// evidence of local verification, not publication approval or remote attestation.
type ApprovedVerificationResult struct {
	CandidateRecordSHA256     string `json:"candidate_record_sha256"`
	LicenseEvidenceSHA256     string `json:"license_evidence_sha256"`
	SHA256SUMSSHA256          string `json:"sha256sums_sha256"`
	TrustRecordSHA256         string `json:"trust_record_sha256"`
	AuthorizationRecordSHA256 string `json:"authorization_record_sha256"`
	KeyID                     string `json:"key_id"`
	KeyFingerprint            string `json:"key_fingerprint"`
	SignatureFileSHA256       string `json:"signature_file_sha256"`
}

// VerifyApproved validates the exact candidate, authorization, trust identity,
// signed checksum set and every artifact through one pinned release root. It
// never extracts, executes, installs, approves, uploads, tags, or publishes.
func VerifyApproved(ctx context.Context, options ApprovedVerificationOptions) (ApprovedVerificationResult, error) {
	var result ApprovedVerificationResult
	if ctx == nil || options.Dir == "" || options.Source == "" || options.CandidateRecordFile == "" ||
		options.LicenseEvidenceFile == "" || options.TrustRecordFile == "" || options.AuthorizationRecordFile == "" ||
		!trustFingerprint(options.ExpectedCandidateSHA256) ||
		!trustFingerprint(options.ExpectedLicenseEvidenceSHA256) || !trustFingerprint(options.ExpectedSumsSHA256) {
		return result, ErrSignature
	}
	if ctx.Err() != nil {
		return result, ErrSignature
	}
	candidateBody, candidate, err := readCandidateRecord(options.CandidateRecordFile)
	if err != nil || prefixedDigest(candidateBody) != options.ExpectedCandidateSHA256 ||
		verifyCandidateRecord(ctx, candidate, options.Source) != nil {
		return result, ErrSignature
	}
	licenseEvidence, err := verifyLicenseEvidenceRecord(ctx, options.LicenseEvidenceFile,
		options.ExpectedLicenseEvidenceSHA256, options.Source)
	if err != nil || licenseEvidence.SourceCommit != candidate.SourceCommit {
		return result, ErrSignature
	}
	source, err := filepath.Abs(options.Source)
	if err != nil {
		return result, ErrSignature
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return result, ErrSignature
	}
	authorization, err := ReadSigningAuthorization(options.AuthorizationRecordFile, SigningAuthorizationExpectations{
		RecordSHA256: options.ExpectedAuthorizationSHA256, CandidateRecordSHA256: options.ExpectedCandidateSHA256,
		LicenseEvidenceSHA256: options.ExpectedLicenseEvidenceSHA256,
		SHA256SUMSSHA256:      options.ExpectedSumsSHA256, TrustRecordSHA256: options.ExpectedTrustRecordSHA256,
		KeyID: options.ExpectedKeyID, KeyFingerprint: options.ExpectedKeyFingerprint,
	})
	if err != nil {
		return result, ErrSignature
	}
	trustRecord, public, err := readExpectedTrustRecord(options.TrustRecordFile, options.ExpectedKeyID,
		options.ExpectedKeyFingerprint, options.ExpectedTrustRecordSHA256)
	if err != nil || authorization.ReleasePolicyURL != trustRecord.ReleasePolicyURL {
		return result, ErrSignature
	}
	root, sums, err := checkedRelease(options.Dir, true)
	if err != nil {
		return result, ErrSignature
	}
	defer root.Close()
	if prefixedDigest(sums) != options.ExpectedSumsSHA256 ||
		approvedArtifactLicenseIdentity(root, candidate, licenseEvidence) != nil {
		return result, ErrSignature
	}
	encodedSignature, err := readReleaseFile(root, signatureName, 129)
	if err != nil {
		return result, ErrSignature
	}
	signature, err := decodeSigningHex(encodedSignature, ed25519.SignatureSize)
	if err != nil || !ed25519.Verify(public, sums, signature) || ctx.Err() != nil {
		return result, ErrSignature
	}
	// Recheck the authenticated set through the same pinned directory after the
	// signature decision. Operators must still keep the directory quiescent.
	if checkReleaseFiles(root, sums, true) != nil {
		return result, ErrSignature
	}
	finalSums, err := readReleaseFile(root, "SHA256SUMS", 64<<10)
	if err != nil || !bytes.Equal(finalSums, sums) {
		return result, ErrSignature
	}
	finalSignature, err := readReleaseFile(root, signatureName, 129)
	if err != nil || !bytes.Equal(finalSignature, encodedSignature) ||
		verifyCandidateCheckout(ctx, source, candidate.SourceCommit, environment()) != nil || ctx.Err() != nil {
		return result, ErrSignature
	}
	result = ApprovedVerificationResult{
		CandidateRecordSHA256: options.ExpectedCandidateSHA256,
		LicenseEvidenceSHA256: options.ExpectedLicenseEvidenceSHA256, SHA256SUMSSHA256: options.ExpectedSumsSHA256,
		TrustRecordSHA256: options.ExpectedTrustRecordSHA256, AuthorizationRecordSHA256: options.ExpectedAuthorizationSHA256,
		KeyID: options.ExpectedKeyID, KeyFingerprint: options.ExpectedKeyFingerprint,
		SignatureFileSHA256: prefixedDigest(encodedSignature),
	}
	return result, nil
}
