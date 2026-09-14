package releasepack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"
)

// PublicationPreflightOptions contains only local public evidence. The
// operation performs no network request and has no tag, upload or release API.
type PublicationPreflightOptions struct {
	Verification                           ApprovedVerificationOptions
	PublicationAuthorizationFile           string
	ExpectedPublicationAuthorizationSHA256 string
	ExpectedRepository                     string
	ReleaseNotesFile                       string
}

type PublicationPreflightResult struct {
	PublicationAuthorizationSHA256 string             `json:"publication_authorization_sha256"`
	Repository                     string             `json:"repository"`
	ReleaseVersion                 string             `json:"release_version"`
	SourceCommit                   string             `json:"source_commit"`
	Tag                            string             `json:"tag"`
	ReleaseTitle                   string             `json:"release_title"`
	TagMessage                     string             `json:"tag_message"`
	Tagger                         PublicationTagger  `json:"tagger"`
	Prerelease                     bool               `json:"prerelease"`
	MakeLatest                     bool               `json:"make_latest"`
	ReleaseNotesSHA256             string             `json:"release_notes_sha256"`
	PublicationApproverID          string             `json:"publication_approver_id"`
	Assets                         []PublicationAsset `json:"assets"`
}

// VerifyPublicationPreflight binds an external publication approval to the
// production-verified local signed set. It does not authorize itself or mutate
// local or remote state.
func VerifyPublicationPreflight(ctx context.Context, options PublicationPreflightOptions) (PublicationPreflightResult, error) {
	return verifyPublicationPreflightWithLicenseEvidenceVerifier(ctx, options, verifyLicenseEvidenceRecord)
}

func verifyPublicationPreflightWithLicenseEvidenceVerifier(ctx context.Context, options PublicationPreflightOptions, verifyEvidence licenseEvidenceRecordVerifier) (PublicationPreflightResult, error) {
	var result PublicationPreflightResult
	if ctx == nil || options.PublicationAuthorizationFile == "" || options.ReleaseNotesFile == "" ||
		!trustFingerprint(options.ExpectedPublicationAuthorizationSHA256) || !githubRepository.MatchString(options.ExpectedRepository) || verifyEvidence == nil {
		return result, ErrPublicationAuthorization
	}
	verification, err := verifyApprovedWithLicenseEvidenceVerifier(ctx, options.Verification, verifyEvidence)
	if err != nil {
		return result, ErrPublicationAuthorization
	}
	candidateBody, candidate, err := readCandidateRecord(options.Verification.CandidateRecordFile)
	if err != nil || prefixedDigest(candidateBody) != verification.CandidateRecordSHA256 {
		return result, ErrPublicationAuthorization
	}
	notes, err := readPublicationInput(options.ReleaseNotesFile, maxReleaseNotes)
	notesRaw := sha256.Sum256(notes)
	if err != nil || len(candidate.SourceCollateral) != 4 || candidate.SourceCollateral[2].Source != "docs/release-notes.md" ||
		candidate.SourceCollateral[2].Entry != releaseNotesName || candidate.SourceCollateral[2].SHA256 != hex.EncodeToString(notesRaw[:]) {
		return result, ErrPublicationAuthorization
	}
	notesSHA := publicationDigest(notes)
	assets, err := publicationAssets(options.Verification.Dir)
	if err != nil {
		return result, ErrPublicationAuthorization
	}
	expected := PublicationAuthorizationExpectations{
		RecordSHA256: options.ExpectedPublicationAuthorizationSHA256, Repository: options.ExpectedRepository,
		ReleaseVersion: candidate.ReleaseVersion, SourceCommit: candidate.SourceCommit, ReleaseNotesSHA256: notesSHA,
		CandidateRecordSHA256: verification.CandidateRecordSHA256,
		LicenseEvidenceSHA256: verification.LicenseEvidenceSHA256, SHA256SUMSSHA256: verification.SHA256SUMSSHA256,
		SignatureFileSHA256: verification.SignatureFileSHA256, TrustRecordSHA256: verification.TrustRecordSHA256,
		SigningAuthorizationSHA256: verification.AuthorizationRecordSHA256,
	}
	authorization, err := ReadPublicationAuthorization(options.PublicationAuthorizationFile, expected)
	if err != nil || !equalPublicationAssets(assets, authorization.Assets) {
		return result, ErrPublicationAuthorization
	}
	// Close the point-in-time observation with the same full approval-bound
	// verification and reject any local source or release mutation during preflight.
	final, err := verifyApprovedWithLicenseEvidenceVerifier(ctx, options.Verification, verifyEvidence)
	if err != nil || final != verification || ctx.Err() != nil {
		return result, ErrPublicationAuthorization
	}
	finalAssets, err := publicationAssets(options.Verification.Dir)
	if err != nil || !equalPublicationAssets(assets, finalAssets) {
		return result, ErrPublicationAuthorization
	}
	return PublicationPreflightResult{
		PublicationAuthorizationSHA256: options.ExpectedPublicationAuthorizationSHA256,
		Repository:                     authorization.Repository, ReleaseVersion: authorization.ReleaseVersion,
		SourceCommit: authorization.SourceCommit, Tag: authorization.Tag, ReleaseTitle: authorization.ReleaseTitle,
		TagMessage: authorization.TagMessage, Tagger: authorization.Tagger,
		Prerelease: authorization.Prerelease, MakeLatest: authorization.MakeLatest,
		ReleaseNotesSHA256: notesSHA, PublicationApproverID: authorization.ApproverID, Assets: assets,
	}, nil
}

func publicationAssets(dir string) ([]PublicationAsset, error) {
	root, _, err := checkedRelease(dir, true)
	if err != nil {
		return nil, ErrPublicationAuthorization
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrPublicationAuthorization
	}
	entries, readErr := directory.ReadDir(16)
	closeErr := directory.Close()
	if readErr != nil && readErr != io.EOF || closeErr != nil || len(entries) != 7 {
		return nil, ErrPublicationAuthorization
	}
	assets := make([]PublicationAsset, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !signingBasename(entry.Name()) {
			return nil, ErrPublicationAuthorization
		}
		file, openErr := openReleaseFile(root, entry.Name())
		if openErr != nil {
			return nil, ErrPublicationAuthorization
		}
		info, statErr := file.Stat()
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(file, (256<<20)+1))
		final, finalErr := file.Stat()
		fileCloseErr := file.Close()
		if statErr != nil || finalErr != nil || fileCloseErr != nil || !os.SameFile(info, final) ||
			n < 1 || n > 256<<20 || total > (512<<20)-n || info.Size() != n || final.Size() != n || copyErr != nil {
			return nil, ErrPublicationAuthorization
		}
		total += n
		assets = append(assets, PublicationAsset{Name: entry.Name(), Size: n, SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil))})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets, nil
}

func readPublicationInput(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > max {
		return nil, ErrPublicationAuthorization
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrPublicationAuthorization
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, ErrPublicationAuthorization
	}
	body, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(body)) != actual.Size() || !canonicalText(body) {
		return nil, ErrPublicationAuthorization
	}
	return body, nil
}

func equalPublicationAssets(a, b []PublicationAsset) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
