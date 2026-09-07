package releasepack

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"time"
)

// ErrTrustRecord deliberately omits the record path and contents. A trust
// record is public, but its retrieval channel and expected identity are policy
// inputs that must not be replaced by untrusted release metadata.
var ErrTrustRecord = errors.New("release trust record validation failed")

const (
	trustRecordSchema = 1
	trustRecordMax    = 16 << 10
	trustScope        = "darwinrouter-release-signing"
)

var trustKeyID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)

// TrustRecord is the canonical, independently published public description of
// a DarwinRouter release-signing identity. It does not authenticate itself:
// operators must obtain it through a separately authenticated channel and bind
// verification to its exact digest, expected KeyID and public-key fingerprint.
type TrustRecord struct {
	SchemaVersion         int    `json:"schema_version"`
	Project               string `json:"project"`
	Scope                 string `json:"scope"`
	KeyID                 string `json:"key_id"`
	Algorithm             string `json:"algorithm"`
	PublicKey             string `json:"public_key"`
	PublicKeySHA256       string `json:"public_key_sha256"`
	Status                string `json:"status"`
	PublishedAt           string `json:"published_at"`
	ReleasePolicyURL      string `json:"release_policy_url"`
	RotationRevocationURL string `json:"rotation_revocation_url"`
}

// ParseTrustRecord accepts only the canonical schema-1 JSON encoding. Strict
// re-encoding rejects duplicate, unknown, aliased and reordered fields.
func ParseTrustRecord(body []byte) (TrustRecord, error) {
	var record TrustRecord
	if len(body) == 0 || len(body) > trustRecordMax || json.Unmarshal(body, &record) != nil {
		return TrustRecord{}, ErrTrustRecord
	}
	canonical, err := json.MarshalIndent(record, "", "  ")
	if err != nil || !bytes.Equal(body, append(canonical, '\n')) || validateTrustRecord(record) != nil {
		return TrustRecord{}, ErrTrustRecord
	}
	return record, nil
}

// VerifyTrustRecord verifies a release with an active record whose exact-byte
// digest, key ID and key fingerprint are supplied independently by the operator.
// The record is never loaded from the release directory and no network discovery
// is performed.
func VerifyTrustRecord(dir, recordFile, expectedKeyID, expectedFingerprint, expectedRecordSHA256 string) error {
	if !trustKeyID.MatchString(expectedKeyID) || !trustFingerprint(expectedFingerprint) || !trustFingerprint(expectedRecordSHA256) {
		return ErrTrustRecord
	}
	body, err := readTrustRecordFile(recordFile)
	if err != nil {
		return ErrTrustRecord
	}
	digest := sha256.Sum256(body)
	if expectedRecordSHA256 != "sha256:"+hex.EncodeToString(digest[:]) {
		return ErrTrustRecord
	}
	record, err := ParseTrustRecord(body)
	if err != nil || record.KeyID != expectedKeyID || record.PublicKeySHA256 != expectedFingerprint || record.Status != "active" {
		return ErrTrustRecord
	}
	key, err := hex.DecodeString(record.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return ErrTrustRecord
	}
	if verifyWithKey(dir, ed25519.PublicKey(key)) != nil {
		return ErrSignature
	}
	return nil
}

func trustFingerprint(value string) bool {
	if len(value) != len("sha256:")+2*sha256.Size || value[:len("sha256:")] != "sha256:" {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && value == "sha256:"+hex.EncodeToString(decoded)
}

func validateTrustRecord(record TrustRecord) error {
	if record.SchemaVersion != trustRecordSchema || record.Project != "DarwinRouter" ||
		record.Scope != trustScope || !trustKeyID.MatchString(record.KeyID) ||
		record.Algorithm != "Ed25519" || (record.Status != "active" && record.Status != "revoked") {
		return ErrTrustRecord
	}
	key, err := hex.DecodeString(record.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || record.PublicKey != bytesToLowerHex(key) {
		return ErrTrustRecord
	}
	digest := sha256.Sum256(key)
	if record.PublicKeySHA256 != "sha256:"+hex.EncodeToString(digest[:]) {
		return ErrTrustRecord
	}
	when, err := time.Parse("2006-01-02T15:04:05Z", record.PublishedAt)
	if err != nil || when.Format("2006-01-02T15:04:05Z") != record.PublishedAt {
		return ErrTrustRecord
	}
	if !trustHTTPSURL(record.ReleasePolicyURL) || !trustHTTPSURL(record.RotationRevocationURL) {
		return ErrTrustRecord
	}
	return nil
}

func trustHTTPSURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		u.Fragment == "" && u.Opaque == "" && u.String() == raw
}

func readTrustRecordFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrTrustRecord
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrTrustRecord
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() {
		return nil, ErrTrustRecord
	}
	body, err := io.ReadAll(io.LimitReader(f, trustRecordMax+1))
	if err != nil || len(body) > trustRecordMax {
		return nil, ErrTrustRecord
	}
	return body, nil
}

func bytesToLowerHex(body []byte) string { return hex.EncodeToString(body) }
