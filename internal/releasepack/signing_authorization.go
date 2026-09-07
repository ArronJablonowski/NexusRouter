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
	"time"
)

var ErrSigningAuthorization = errors.New("release signing authorization validation failed")

const (
	signingAuthorizationSchema = 2
	signingAuthorizationScope  = "darwinrouter-release-signing-authorization"
	maxSigningAuthorization    = 16 << 10
)

var signingApproverID = regexp.MustCompile(`^[a-z0-9][a-z0-9._:@/-]{1,126}[a-z0-9]$`)

// SigningAuthorization is an operator-authored approval of one exact candidate,
// checksum manifest and public trust identity. Publication remains unapproved:
// this record grants only the narrower authority to create a release signature.
type SigningAuthorization struct {
	SchemaVersion         int                          `json:"schema_version"`
	Project               string                       `json:"project"`
	Scope                 string                       `json:"scope"`
	CandidateRecordSHA256 string                       `json:"candidate_record_sha256"`
	SHA256SUMSSHA256      string                       `json:"sha256sums_sha256"`
	TrustRecordSHA256     string                       `json:"trust_record_sha256"`
	KeyID                 string                       `json:"key_id"`
	KeyFingerprint        string                       `json:"key_fingerprint"`
	Targets               []SigningAuthorizationTarget `json:"targets"`
	Gates                 []SigningAuthorizationGate   `json:"gates"`
	ApproverID            string                       `json:"approver_id"`
	ReleasePolicyURL      string                       `json:"release_policy_url"`
	ApprovedAt            string                       `json:"approved_at"`
}

type SigningAuthorizationTarget struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Decision string `json:"decision"`
}

type SigningAuthorizationGate struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SigningAuthorizationExpectations are obtained independently of the record.
// Every field is mandatory; RecordSHA256 binds the exact canonical bytes.
type SigningAuthorizationExpectations struct {
	RecordSHA256          string
	CandidateRecordSHA256 string
	SHA256SUMSSHA256      string
	TrustRecordSHA256     string
	KeyID                 string
	KeyFingerprint        string
}

var authorizedTargets = []SigningAuthorizationTarget{
	{OS: "darwin", Arch: "amd64", Decision: "supported"},
	{OS: "darwin", Arch: "arm64", Decision: "supported"},
	{OS: "linux", Arch: "amd64", Decision: "supported"},
	{OS: "linux", Arch: "arm64", Decision: "supported"},
}

var signingAuthorizationGates = []SigningAuthorizationGate{
	{Name: "project_license", Status: "approved"},
	{Name: "third_party_notices", Status: "approved"},
	{Name: "production_signing", Status: "approved"},
	{Name: "publication", Status: "unapproved"},
}

// ParseSigningAuthorization accepts only the canonical schema-1 JSON encoding.
// Re-encoding rejects unknown, duplicate, aliased and reordered fields.
func ParseSigningAuthorization(body []byte) (SigningAuthorization, error) {
	var record SigningAuthorization
	if len(body) == 0 || len(body) > maxSigningAuthorization || json.Unmarshal(body, &record) != nil {
		return SigningAuthorization{}, ErrSigningAuthorization
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) || validateSigningAuthorization(record) != nil {
		return SigningAuthorization{}, ErrSigningAuthorization
	}
	return record, nil
}

// ReadSigningAuthorization reads a regular nonsymlink file and accepts it only
// when the caller independently supplies the exact record digest and every
// public input that the signing approval binds.
func ReadSigningAuthorization(path string, expected SigningAuthorizationExpectations) (SigningAuthorization, error) {
	if !validSigningAuthorizationExpectations(expected) {
		return SigningAuthorization{}, ErrSigningAuthorization
	}
	body, err := readSigningAuthorizationFile(path)
	if err != nil || prefixedSigningAuthorizationDigest(body) != expected.RecordSHA256 {
		return SigningAuthorization{}, ErrSigningAuthorization
	}
	record, err := ParseSigningAuthorization(body)
	if err != nil || record.CandidateRecordSHA256 != expected.CandidateRecordSHA256 ||
		record.SHA256SUMSSHA256 != expected.SHA256SUMSSHA256 ||
		record.TrustRecordSHA256 != expected.TrustRecordSHA256 || record.KeyID != expected.KeyID ||
		record.KeyFingerprint != expected.KeyFingerprint {
		return SigningAuthorization{}, ErrSigningAuthorization
	}
	return record, nil
}

func validateSigningAuthorization(record SigningAuthorization) error {
	if record.SchemaVersion != signingAuthorizationSchema || record.Project != "DarwinRouter" ||
		record.Scope != signingAuthorizationScope || !trustFingerprint(record.CandidateRecordSHA256) ||
		!trustFingerprint(record.SHA256SUMSSHA256) || !trustFingerprint(record.TrustRecordSHA256) ||
		!trustKeyID.MatchString(record.KeyID) || !trustFingerprint(record.KeyFingerprint) ||
		len(record.Targets) != len(authorizedTargets) || len(record.Gates) != len(signingAuthorizationGates) ||
		!signingApproverID.MatchString(record.ApproverID) || !trustHTTPSURL(record.ReleasePolicyURL) {
		return ErrSigningAuthorization
	}
	for i := range authorizedTargets {
		if record.Targets[i] != authorizedTargets[i] {
			return ErrSigningAuthorization
		}
	}
	for i := range signingAuthorizationGates {
		if record.Gates[i] != signingAuthorizationGates[i] {
			return ErrSigningAuthorization
		}
	}
	approved, err := time.Parse("2006-01-02T15:04:05Z", record.ApprovedAt)
	if err != nil || approved.Before(time.Unix(0, 0)) || approved.Format("2006-01-02T15:04:05Z") != record.ApprovedAt {
		return ErrSigningAuthorization
	}
	return nil
}

func validSigningAuthorizationExpectations(expected SigningAuthorizationExpectations) bool {
	return trustFingerprint(expected.RecordSHA256) && trustFingerprint(expected.CandidateRecordSHA256) &&
		trustFingerprint(expected.SHA256SUMSSHA256) && trustFingerprint(expected.TrustRecordSHA256) &&
		trustKeyID.MatchString(expected.KeyID) && trustFingerprint(expected.KeyFingerprint)
}

func readSigningAuthorizationFile(path string) ([]byte, error) {
	if path == "" {
		return nil, ErrSigningAuthorization
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxSigningAuthorization {
		return nil, ErrSigningAuthorization
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrSigningAuthorization
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return nil, ErrSigningAuthorization
	}
	body, err := io.ReadAll(io.LimitReader(file, maxSigningAuthorization+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return nil, ErrSigningAuthorization
	}
	return body, nil
}

func prefixedSigningAuthorizationDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
