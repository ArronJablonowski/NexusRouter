package releasepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrPublicationAuthorization = errors.New("release publication authorization validation failed")

const (
	publicationAuthorizationSchema = 1
	publicationAuthorizationScope  = "darwinrouter-github-publication-authorization"
	maxPublicationAuthorization    = 32 << 10
)

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

// PublicationAuthorization is an externally authored approval to publish one
// exact signed set. It is data only; this package provides no approval generator.
type PublicationAuthorization struct {
	SchemaVersion              int                            `json:"schema_version"`
	Project                    string                         `json:"project"`
	Scope                      string                         `json:"scope"`
	GitHubHost                 string                         `json:"github_host"`
	Repository                 string                         `json:"repository"`
	ReleaseVersion             string                         `json:"release_version"`
	SourceCommit               string                         `json:"source_commit"`
	Tag                        string                         `json:"tag"`
	ReleaseTitle               string                         `json:"release_title"`
	Prerelease                 bool                           `json:"prerelease"`
	MakeLatest                 bool                           `json:"make_latest"`
	ReleaseNotesSHA256         string                         `json:"release_notes_sha256"`
	CandidateRecordSHA256      string                         `json:"candidate_record_sha256"`
	LicenseEvidenceSHA256      string                         `json:"license_evidence_sha256"`
	SHA256SUMSSHA256           string                         `json:"sha256sums_sha256"`
	SignatureFileSHA256        string                         `json:"signature_file_sha256"`
	TrustRecordSHA256          string                         `json:"trust_record_sha256"`
	SigningAuthorizationSHA256 string                         `json:"signing_authorization_sha256"`
	Assets                     []PublicationAsset             `json:"assets"`
	Controls                   PublicationControls            `json:"controls"`
	Gates                      []PublicationAuthorizationGate `json:"gates"`
	ApproverID                 string                         `json:"approver_id"`
	PublicationPolicyURL       string                         `json:"publication_policy_url"`
	ApprovedAt                 string                         `json:"approved_at"`
}

type PublicationAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type PublicationControls struct {
	DraftFirst        bool `json:"draft_first"`
	CreateOnly        bool `json:"create_only"`
	ImmutableRequired bool `json:"immutable_required"`
}

type PublicationAuthorizationGate struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

var publicationAuthorizationGates = []PublicationAuthorizationGate{
	{Name: "signed_artifacts", Status: "approved"},
	{Name: "independent_verification", Status: "approved"},
	{Name: "publication", Status: "approved"},
}

type PublicationAuthorizationExpectations struct {
	RecordSHA256               string
	Repository                 string
	ReleaseVersion             string
	SourceCommit               string
	ReleaseNotesSHA256         string
	CandidateRecordSHA256      string
	LicenseEvidenceSHA256      string
	SHA256SUMSSHA256           string
	SignatureFileSHA256        string
	TrustRecordSHA256          string
	SigningAuthorizationSHA256 string
}

func ParsePublicationAuthorization(body []byte) (PublicationAuthorization, error) {
	var record PublicationAuthorization
	if len(body) == 0 || len(body) > maxPublicationAuthorization || json.Unmarshal(body, &record) != nil {
		return PublicationAuthorization{}, ErrPublicationAuthorization
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) || validatePublicationAuthorization(record) != nil {
		return PublicationAuthorization{}, ErrPublicationAuthorization
	}
	return record, nil
}

func ReadPublicationAuthorization(path string, expected PublicationAuthorizationExpectations) (PublicationAuthorization, error) {
	if !validPublicationExpectations(expected) {
		return PublicationAuthorization{}, ErrPublicationAuthorization
	}
	body, err := readPublicationAuthorizationFile(path)
	if err != nil || publicationDigest(body) != expected.RecordSHA256 {
		return PublicationAuthorization{}, ErrPublicationAuthorization
	}
	record, err := ParsePublicationAuthorization(body)
	if err != nil || record.Repository != expected.Repository || record.ReleaseVersion != expected.ReleaseVersion ||
		record.SourceCommit != expected.SourceCommit || record.ReleaseNotesSHA256 != expected.ReleaseNotesSHA256 ||
		record.CandidateRecordSHA256 != expected.CandidateRecordSHA256 ||
		record.LicenseEvidenceSHA256 != expected.LicenseEvidenceSHA256 ||
		record.SHA256SUMSSHA256 != expected.SHA256SUMSSHA256 ||
		record.SignatureFileSHA256 != expected.SignatureFileSHA256 ||
		record.TrustRecordSHA256 != expected.TrustRecordSHA256 ||
		record.SigningAuthorizationSHA256 != expected.SigningAuthorizationSHA256 {
		return PublicationAuthorization{}, ErrPublicationAuthorization
	}
	return record, nil
}

func validatePublicationAuthorization(record PublicationAuthorization) error {
	if record.SchemaVersion != publicationAuthorizationSchema || record.Project != "DarwinRouter" ||
		record.Scope != publicationAuthorizationScope || record.GitHubHost != "github.com" ||
		!githubRepository.MatchString(record.Repository) || validate(Options{Version: record.ReleaseVersion, Commit: record.SourceCommit, Out: "release"}) != nil ||
		record.Tag != "v"+record.ReleaseVersion || record.ReleaseTitle != "DarwinRouter "+record.Tag ||
		record.Prerelease != strings.Contains(record.ReleaseVersion, "-") || (record.Prerelease && record.MakeLatest) ||
		!trustFingerprint(record.ReleaseNotesSHA256) || !trustFingerprint(record.CandidateRecordSHA256) ||
		!trustFingerprint(record.LicenseEvidenceSHA256) || !trustFingerprint(record.SHA256SUMSSHA256) ||
		!trustFingerprint(record.SignatureFileSHA256) || !trustFingerprint(record.TrustRecordSHA256) ||
		!trustFingerprint(record.SigningAuthorizationSHA256) || len(record.Assets) != 7 ||
		!record.Controls.DraftFirst || !record.Controls.CreateOnly || !record.Controls.ImmutableRequired ||
		len(record.Gates) != len(publicationAuthorizationGates) || !signingApproverID.MatchString(record.ApproverID) ||
		!trustHTTPSURL(record.PublicationPolicyURL) {
		return ErrPublicationAuthorization
	}
	previous := ""
	names := publicationAssetNames(record.ReleaseVersion)
	for i, asset := range record.Assets {
		if asset.Name != names[i] || !signingBasename(asset.Name) || asset.Name <= previous || asset.Size < 1 || asset.Size > 4<<30 || !trustFingerprint(asset.SHA256) {
			return ErrPublicationAuthorization
		}
		previous = asset.Name
	}
	for i := range publicationAuthorizationGates {
		if record.Gates[i] != publicationAuthorizationGates[i] {
			return ErrPublicationAuthorization
		}
	}
	approved, err := time.Parse("2006-01-02T15:04:05Z", record.ApprovedAt)
	if err != nil || approved.Before(time.Unix(0, 0)) || approved.Format("2006-01-02T15:04:05Z") != record.ApprovedAt {
		return ErrPublicationAuthorization
	}
	return nil
}

func publicationAssetNames(version string) []string {
	names := []string{"SHA256SUMS", signatureName, "manifest.json"}
	for _, target := range licenseEvidenceTargets {
		names = append(names, "DarwinRouter_"+version+"_"+target[0]+"_"+target[1]+".tar.gz")
	}
	sort.Strings(names)
	return names
}

func validPublicationExpectations(e PublicationAuthorizationExpectations) bool {
	return trustFingerprint(e.RecordSHA256) && githubRepository.MatchString(e.Repository) &&
		validate(Options{Version: e.ReleaseVersion, Commit: e.SourceCommit, Out: "release"}) == nil &&
		trustFingerprint(e.ReleaseNotesSHA256) && trustFingerprint(e.CandidateRecordSHA256) &&
		trustFingerprint(e.LicenseEvidenceSHA256) && trustFingerprint(e.SHA256SUMSSHA256) &&
		trustFingerprint(e.SignatureFileSHA256) && trustFingerprint(e.TrustRecordSHA256) &&
		trustFingerprint(e.SigningAuthorizationSHA256)
}

func readPublicationAuthorizationFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxPublicationAuthorization {
		return nil, ErrPublicationAuthorization
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrPublicationAuthorization
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, ErrPublicationAuthorization
	}
	body, err := io.ReadAll(io.LimitReader(f, maxPublicationAuthorization+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return nil, ErrPublicationAuthorization
	}
	return body, nil
}

func publicationDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
